package opsrepo

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

// setDeadline sets one deadline column of an errand fixture, and its version.
func setDeadline(t *testing.T, conn *pgx.Conn, id, column string, at time.Time, version int) {
	t.Helper()
	_, err := conn.Exec(context.Background(),
		`UPDATE errands SET `+pgx.Identifier{column}.Sanitize()+` = $2, version = $3 WHERE id = $1`, id, at, version)
	require.NoError(t, err)
}

func candidateIDs(candidates []ReminderCandidate) []string {
	ids := make([]string, len(candidates))
	for i, c := range candidates {
		ids[i] = c.ErrandID
	}
	return ids
}

func TestListReminderCandidates(t *testing.T) {
	repo, conn := migratedRepository(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	remindBefore := now.Add(25 * time.Minute)

	soon := insertErrand(t, conn, errandRow{status: "DELIVERED", courier: "courier-1"})
	setDeadline(t, conn, soon, "verification_deadline", now.Add(20*time.Minute), 6)
	atEdge := insertErrand(t, conn, errandRow{status: "DELIVERED", courier: "courier-1"})
	setDeadline(t, conn, atEdge, "verification_deadline", remindBefore, 6)
	tooEarly := insertErrand(t, conn, errandRow{status: "DELIVERED", courier: "courier-1"})
	setDeadline(t, conn, tooEarly, "verification_deadline", remindBefore.Add(time.Second), 6)
	lapsed := insertErrand(t, conn, errandRow{status: "DELIVERED", courier: "courier-1"})
	setDeadline(t, conn, lapsed, "verification_deadline", now, 6)
	completed := insertErrand(t, conn, errandRow{status: "COMPLETED", courier: "courier-1"})
	setDeadline(t, conn, completed, "verification_deadline", now.Add(20*time.Minute), 7)
	assigned := insertErrand(t, conn, errandRow{status: "ASSIGNED", courier: "courier-2"})
	setDeadline(t, conn, assigned, "pickup_deadline", now.Add(5*time.Minute), 3)

	got, err := repo.ListReminderCandidates(ctx, ReminderVerification, now, remindBefore, 10)
	require.NoError(t, err)
	require.Equal(t, []string{soon, atEdge}, candidateIDs(got), "after now, at or before remindBefore, DELIVERED only, by deadline")
	require.Equal(t, ReminderCandidate{
		ErrandID: soon, Version: 6, Status: errand.StatusDelivered,
		RequesterID: "requester-1", CourierID: "courier-1", Deadline: now.Add(20 * time.Minute),
	}, normalize(got[0]))

	got, err = repo.ListReminderCandidates(ctx, ReminderVerification, now, remindBefore, 1)
	require.NoError(t, err)
	require.Equal(t, []string{soon}, candidateIDs(got))

	got, err = repo.ListReminderCandidates(ctx, ReminderPickup, now, now.Add(10*time.Minute), 10)
	require.NoError(t, err)
	require.Equal(t, []string{assigned}, candidateIDs(got))
	require.Equal(t, "courier-2", got[0].CourierID)

	_, err = repo.ListReminderCandidates(ctx, "nudge", now, remindBefore, 10)
	require.ErrorIs(t, err, ErrInvalidArgument)
	_, err = repo.ListReminderCandidates(ctx, ReminderPickup, now, remindBefore, 0)
	require.ErrorIs(t, err, ErrInvalidArgument)
}

func TestInsertCurrentReminder(t *testing.T) {
	repo, conn := migratedRepository(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	id := insertErrand(t, conn, errandRow{status: "DELIVERED", courier: "courier-1"})
	setDeadline(t, conn, id, "verification_deadline", now.Add(20*time.Minute), 6)
	row := func(version int) errand.OutboxRow {
		return errand.OutboxRow{
			Kind:           errand.ErrandVerificationReminder.Kind(),
			IdempotencyKey: errand.IdempotencyKey(id, "verify-reminder", version),
			ErrandID:       id,
			Payload:        map[string]any{"errandId": id, "errandVersion": version},
		}
	}
	list := func() []string {
		got, err := repo.ListReminderCandidates(ctx, ReminderVerification, now, now.Add(25*time.Minute), 10)
		require.NoError(t, err)
		return candidateIDs(got)
	}

	inserted, err := repo.InsertCurrentReminder(ctx, row(5), errand.StatusDelivered, 5)
	require.NoError(t, err)
	require.False(t, inserted, "stale version")
	inserted, err = repo.InsertCurrentReminder(ctx, row(6), errand.StatusAssigned, 6)
	require.NoError(t, err)
	require.False(t, inserted, "wrong status")

	require.Equal(t, []string{id}, list())
	inserted, err = repo.InsertCurrentReminder(ctx, row(6), errand.StatusDelivered, 6)
	require.NoError(t, err)
	require.True(t, inserted)
	inserted, err = repo.InsertCurrentReminder(ctx, row(6), errand.StatusDelivered, 6)
	require.NoError(t, err)
	require.False(t, inserted, "duplicate key")
	require.Empty(t, list(), "an errand with a reminder for its version is no longer a candidate")

	setDeadline(t, conn, id, "verification_deadline", now.Add(20*time.Minute), 7)
	require.Equal(t, []string{id}, list(), "a new version is a candidate again")

	var count int
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT count(*) FROM outbox WHERE errand_id = $1 AND kind = $2 AND status = 'PENDING'`,
		id, errand.ErrandVerificationReminder.Kind()).Scan(&count))
	require.Equal(t, 1, count)

	missing := row(1)
	missing.ErrandID = newUUID(t)
	missing.IdempotencyKey = missing.ErrandID + ":verify-reminder:1"
	inserted, err = repo.InsertCurrentReminder(ctx, missing, errand.StatusDelivered, 1)
	require.NoError(t, err)
	require.False(t, inserted, "unknown errand")

	notReminder := row(6)
	notReminder.Kind = errand.ErrandCompleted.Kind()
	_, err = repo.InsertCurrentReminder(ctx, notReminder, errand.StatusDelivered, 6)
	require.ErrorIs(t, err, ErrInvalidArgument)
}

// normalize puts a candidate's deadline in UTC so it compares with ==.
func normalize(c ReminderCandidate) ReminderCandidate {
	c.Deadline = c.Deadline.UTC()
	return c
}
