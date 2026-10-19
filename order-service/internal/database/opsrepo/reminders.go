package opsrepo

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/opsgen"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

// InsertReminder writes a reminder event row to the outbox. It is the only
// outbox write B makes (docs/contracts.md, "Who touches which rows"). A row
// with the same idempotency key already present is left as it is and
// inserted is false (NFR7.3). An unknown errand gives ErrNotFound.
func (r *Repository) InsertReminder(ctx context.Context, row errand.OutboxRow) (inserted bool, err error) {
	params, err := reminderParams(row)
	if err != nil {
		return false, err
	}
	affected, err := r.queries.InsertReminder(ctx, opsgen.InsertReminderParams{
		Kind:           params.Kind,
		IdempotencyKey: params.IdempotencyKey,
		ErrandID:       params.ErrandID,
		Payload:        params.Payload,
	})
	if err != nil {
		return false, mapError(err)
	}
	return affected == 1, nil
}

// InsertCurrentReminder is InsertReminder for a reminder raised for one
// state of the errand: the row is written only if the errand still has
// status and version, waiting for a transition in progress to finish first.
// inserted is false when the errand has moved on, does not exist, or already
// has the row.
func (r *Repository) InsertCurrentReminder(ctx context.Context, row errand.OutboxRow, status errand.Status, version int) (inserted bool, err error) {
	params, err := reminderParams(row)
	if err != nil {
		return false, err
	}
	params.Status = string(status)
	params.Version = int32(version)
	affected, err := r.queries.InsertCurrentReminder(ctx, params)
	if err != nil {
		return false, mapError(err)
	}
	return affected == 1, nil
}

func reminderParams(row errand.OutboxRow) (opsgen.InsertCurrentReminderParams, error) {
	if !isReminderKind(row.Kind) {
		return opsgen.InsertCurrentReminderParams{}, invalid("%q is not a reminder kind", row.Kind)
	}
	if row.IdempotencyKey == "" {
		return opsgen.InsertCurrentReminderParams{}, invalid("idempotency key is required")
	}
	errandID, err := parseUUID("errandId", row.ErrandID)
	if err != nil {
		return opsgen.InsertCurrentReminderParams{}, err
	}
	payload, err := json.Marshal(row.Payload)
	if err != nil {
		return opsgen.InsertCurrentReminderParams{}, invalid("payload cannot be encoded: %v", err)
	}
	return opsgen.InsertCurrentReminderParams{
		Kind:           row.Kind,
		IdempotencyKey: row.IdempotencyKey,
		ErrandID:       errandID,
		Payload:        payload,
	}, nil
}

func isReminderKind(kind string) bool {
	return kind == errand.ErrandVerificationReminder.Kind() ||
		kind == errand.ErrandPickupDeadlineApproaching.Kind()
}

// ReminderKind selects a reminder candidate query.
type ReminderKind string

const (
	// ReminderVerification: DELIVERED errands by verification deadline,
	// without a {errandId}:verify-reminder:{version} row (F3.10.3).
	ReminderVerification ReminderKind = "verification"
	// ReminderPickup: ASSIGNED errands by pickup deadline, without a
	// {errandId}:pickup-reminder:{version} row.
	ReminderPickup ReminderKind = "pickup"
)

// ReminderCandidate is an errand that is due a reminder, as it was when
// selected.
type ReminderCandidate struct {
	ErrandID    string
	Version     int
	Status      errand.Status
	RequesterID string
	CourierID   string
	// Deadline is the verification or pickup deadline the reminder is about.
	Deadline time.Time
}

// ListReminderCandidates returns up to limit errands due a reminder of kind:
// in the kind's state, with its deadline after now and at or before
// remindBefore, and with no reminder row for their current version yet.
// They are ordered by deadline, then ID. The query reads errands and outbox
// and writes nothing.
func (r *Repository) ListReminderCandidates(ctx context.Context, kind ReminderKind, now, remindBefore time.Time, limit int) ([]ReminderCandidate, error) {
	if err := checkLimit(limit); err != nil {
		return nil, err
	}
	var (
		candidates []ReminderCandidate
		err        error
	)
	add := func(id pgtype.UUID, version int32, status, requester string, courier pgtype.Text, deadline pgtype.Timestamptz) {
		candidates = append(candidates, ReminderCandidate{
			ErrandID:    uuidString(id),
			Version:     int(version),
			Status:      errand.Status(status),
			RequesterID: requester,
			CourierID:   courier.String,
			Deadline:    timeOrZero(deadline),
		})
	}
	switch kind {
	case ReminderVerification:
		var rows []opsgen.ListVerificationReminderCandidatesRow
		rows, err = r.queries.ListVerificationReminderCandidates(ctx, opsgen.ListVerificationReminderCandidatesParams{
			Now: timestamp(now), RemindBefore: timestamp(remindBefore), RowLimit: int32(limit),
		})
		for _, row := range rows {
			add(row.ID, row.Version, row.Status, row.RequesterID, row.CourierID, row.Deadline)
		}
	case ReminderPickup:
		var rows []opsgen.ListPickupReminderCandidatesRow
		rows, err = r.queries.ListPickupReminderCandidates(ctx, opsgen.ListPickupReminderCandidatesParams{
			Now: timestamp(now), RemindBefore: timestamp(remindBefore), RowLimit: int32(limit),
		})
		for _, row := range rows {
			add(row.ID, row.Version, row.Status, row.RequesterID, row.CourierID, row.Deadline)
		}
	default:
		return nil, invalid("unknown reminder kind %q", kind)
	}
	if err != nil {
		return nil, mapError(err)
	}
	return candidates, nil
}
