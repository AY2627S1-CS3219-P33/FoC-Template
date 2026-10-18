package errandrepo_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/errandrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/migrations"
)

func TestCreate(t *testing.T) {
	s, pool := newStore(t)
	ctx := context.Background()
	supplier := newUUID(t)
	e := newErrand(t, supplier)
	tr := submitTransition(t, e, t0)

	stored, created, err := s.Create(ctx, e, tr)
	require.NoError(t, err)
	require.True(t, created)
	assert.Equal(t, e.ID, stored.ID)
	assert.Equal(t, errand.StatusPendingReservation, stored.Status)
	assert.Equal(t, 1, stored.Version)
	assert.Equal(t, supplier, stored.Details.SupplierID)
	assert.JSONEq(t, string(e.Details.Items), string(stored.Details.Items))
	assert.True(t, t0.Equal(stored.CreatedAt), "created_at is the transition time")

	got, err := s.Get(ctx, e.ID)
	require.NoError(t, err)
	assert.Equal(t, stored, got)
	assert.Equal(t, []string{"credit.reserve", "event.errand.placed"}, outboxKinds(t, pool, e.ID))
	assert.Equal(t, []string{e.ID + ":reserve", e.ID + ":placed:1"}, outboxKeys(t, pool, e.ID))
	var from *string
	var to, event string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT from_status, to_status, event FROM errand_transitions WHERE errand_id = $1`, e.ID,
	).Scan(&from, &to, &event))
	assert.Nil(t, from)
	assert.Equal(t, "PENDING_RESERVATION", to)
	assert.Equal(t, "submit", event)

	t.Run("a replayed key returns the original and writes nothing", func(t *testing.T) {
		replay := newErrand(t, supplier)
		replay.CreateIdempotencyKey = e.CreateIdempotencyKey
		again, created, err := s.Create(ctx, replay, submitTransition(t, replay, t0))
		require.NoError(t, err)
		assert.False(t, created)
		assert.Equal(t, e.ID, again.ID)
		_, err = s.Get(ctx, replay.ID)
		requireCode(t, apperror.ErrandNotFound, err)
		assert.Len(t, outboxKinds(t, pool, e.ID), 2)
	})

	t.Run("a fenced supplier is refused", func(t *testing.T) {
		fenced := newUUID(t)
		insertFence(t, pool, fenced, "OPEN")
		f := newErrand(t, fenced)
		_, _, err := s.Create(ctx, f, submitTransition(t, f, t0))
		requireCode(t, apperror.SupplierDeletionInProgress, err)
		_, err = s.Get(ctx, f.ID)
		requireCode(t, apperror.ErrandNotFound, err)

		committed := newUUID(t)
		insertFence(t, pool, committed, "COMMITTED")
		f = newErrand(t, committed)
		_, _, err = s.Create(ctx, f, submitTransition(t, f, t0))
		requireCode(t, apperror.SupplierDeletionInProgress, err)

		released := newUUID(t)
		insertFence(t, pool, released, "RELEASED")
		f = newErrand(t, released)
		_, created, err := s.Create(ctx, f, submitTransition(t, f, t0))
		require.NoError(t, err)
		assert.True(t, created, "a released fence no longer blocks")
	})

	t.Run("only Submit transitions are created", func(t *testing.T) {
		f := newErrand(t, supplier)
		tr := submitTransition(t, f, t0)
		tr.ErrandID = newUUID(t)
		_, _, err := s.Create(ctx, f, tr)
		requireCode(t, apperror.InvalidArgument, err)
	})
}

func TestCreateWaitsForFenceAcquire(t *testing.T) {
	s, pool := newStore(t)
	ctx := context.Background()
	supplier := newUUID(t)

	// Fence Acquire steps 1 and 2, not yet committed.
	acquire, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = acquire.Rollback(ctx) }()
	_, err = acquire.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('supplier:' || $1::uuid::text, 0))`, supplier)
	require.NoError(t, err)
	_, err = acquire.Exec(ctx, `INSERT INTO supplier_fences (operation_id, supplier_id) VALUES (gen_random_uuid(), $1)`, supplier)
	require.NoError(t, err)

	e := newErrand(t, supplier)
	tr := submitTransition(t, e, t0)
	done := make(chan error, 1)
	go func() {
		_, _, err := s.Create(ctx, e, tr)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("Create did not wait for the exclusive supplier lock: %v", err)
	case <-time.After(300 * time.Millisecond):
	}

	require.NoError(t, acquire.Commit(ctx))
	select {
	case err := <-done:
		requireCode(t, apperror.SupplierDeletionInProgress, err)
	case <-time.After(10 * time.Second):
		t.Fatal("Create still waiting after the fence committed")
	}
}

func TestApplyCompareAndSwap(t *testing.T) {
	s, pool := newStore(t)
	ctx := context.Background()

	t.Run("second accept from the same snapshot is ERRAND_UNAVAILABLE", func(t *testing.T) {
		snapshot := openErrand(t, s, newUUID(t))
		first := next(t, snapshot, errand.EventAccept, courier("courier-a"), acceptInput(), t0)
		second := next(t, snapshot, errand.EventAccept, courier("courier-b"), acceptInput(), t0)

		stored, err := s.Apply(ctx, first)
		require.NoError(t, err)
		assert.Equal(t, errand.StatusAssigned, stored.Status)
		assert.Equal(t, "courier-a", stored.CourierID)
		assert.NotNil(t, stored.PickupDeadline)
		rows := len(outboxKinds(t, pool, snapshot.ID))

		_, err = s.Apply(ctx, second)
		requireCode(t, apperror.ErrandUnavailable, err)
		got, err := s.Get(ctx, snapshot.ID)
		require.NoError(t, err)
		assert.Equal(t, stored, got)
		assert.Len(t, outboxKinds(t, pool, snapshot.ID), rows, "a failed apply writes no outbox rows")
	})

	t.Run("stale version with the same status is ERRAND_CHANGED", func(t *testing.T) {
		snapshot := openErrand(t, s, newUUID(t))
		edit := snapshot.Details
		edit.Notes = "first"
		_, err := s.Apply(ctx, next(t, snapshot, errand.EventEdit, requester(), errand.EventInput{Edit: &edit}, t0))
		require.NoError(t, err)

		edit.Notes = "stale"
		_, err = s.Apply(ctx, next(t, snapshot, errand.EventEdit, requester(), errand.EventInput{Edit: &edit}, t0))
		requireCode(t, apperror.ErrandChanged, err)
		got, err := s.Get(ctx, snapshot.ID)
		require.NoError(t, err)
		assert.Equal(t, "first", got.Details.Notes)
	})

	t.Run("a pending reward edit is stored and is part of the compare", func(t *testing.T) {
		snapshot := openErrand(t, s, newUUID(t))
		edit := snapshot.Details
		edit.Reward = snapshot.Details.Reward + 5
		stored, err := s.Apply(ctx, next(t, snapshot, errand.EventEdit, requester(), errand.EventInput{Edit: &edit}, t0))
		require.NoError(t, err)
		require.NotNil(t, stored.PendingEdit)
		assert.Equal(t, snapshot.Version, stored.Version, "a reward edit keeps the version")
		assert.Equal(t, edit.Reward, stored.PendingEdit.Details.Reward)
		assert.Equal(t, errand.IdempotencyKey(snapshot.ID, "adjust", snapshot.Version), stored.PendingEdit.Key)
		assert.Equal(t, snapshot.Details.Reward, stored.Details.Reward)

		// The version is unchanged, but the snapshot has no pending edit.
		_, err = s.Apply(ctx, next(t, snapshot, errand.EventCancel, requester(), errand.EventInput{}, t0))
		requireCode(t, apperror.ErrandChanged, err)

		confirmed, err := s.Apply(ctx, next(t, stored, errand.EventEditConfirmed, errand.SystemActor(),
			errand.EventInput{EditKey: stored.PendingEdit.Key}, t0))
		require.NoError(t, err)
		assert.Nil(t, confirmed.PendingEdit)
		assert.Equal(t, edit.Reward, confirmed.Details.Reward)
	})

	t.Run("unknown errand and Submit are refused", func(t *testing.T) {
		snapshot := openErrand(t, s, newUUID(t))
		tr := next(t, snapshot, errand.EventCancel, requester(), errand.EventInput{}, t0)
		tr.ErrandID = newUUID(t)
		_, err := s.Apply(ctx, tr)
		requireCode(t, apperror.ErrandNotFound, err)
		tr.ErrandID = "not-a-uuid"
		_, err = s.Apply(ctx, tr)
		requireCode(t, apperror.ErrandNotFound, err)

		e := newErrand(t, newUUID(t))
		_, err = s.Apply(ctx, submitTransition(t, e, t0))
		requireCode(t, apperror.InvalidArgument, err)
	})
}

// F3.3.5: accepts, a cancel and the expiry job race for the same errands, and
// exactly one of them wins each errand.
func TestConcurrentAcceptCancelExpiryHasOneWinner(t *testing.T) {
	s, pool := newStore(t)
	ctx := context.Background()
	const n = 20
	supplier := newUUID(t)
	expireAt := t0.Add(2 * time.Hour)

	start := make(chan struct{})
	var wg sync.WaitGroup
	var applied, claimed atomic.Int64
	errs := make(chan error, 4*n)
	run := func(f func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := f(); err != nil {
				errs <- err
			}
		}()
	}
	apply := func(tr errand.Transition) {
		run(func() error {
			_, err := s.Apply(ctx, tr)
			if err == nil {
				applied.Add(1)
			}
			return err
		})
	}

	ids := make([]string, n)
	for i := range ids {
		snapshot := openErrand(t, s, supplier)
		ids[i] = snapshot.ID
		apply(next(t, snapshot, errand.EventAccept, courier("courier-a"), acceptInput(), t0))
		apply(next(t, snapshot, errand.EventAccept, courier("courier-b"), acceptInput(), t0))
		apply(next(t, snapshot, errand.EventCancel, requester(), errand.EventInput{}, t0))
	}
	for range 2 {
		run(func() error {
			got, err := s.ClaimDue(ctx, errand.AcceptanceExpiry, expireAt, n, expire(expireAt))
			claimed.Add(int64(got))
			return err
		})
	}
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		requireCode(t, apperror.ErrandUnavailable, err)
	}
	assert.EqualValues(t, n, applied.Load()+claimed.Load(), "one winner per errand")

	for _, id := range ids {
		e, err := s.Get(ctx, id)
		require.NoError(t, err)
		assert.Contains(t, []errand.Status{errand.StatusAssigned, errand.StatusCancelled, errand.StatusExpired}, e.Status)
		assert.Equal(t, 3, e.Version)

		var fromOpen int
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT count(*) FROM errand_transitions WHERE errand_id = $1 AND from_status = 'OPEN'`, id,
		).Scan(&fromOpen))
		assert.Equal(t, 1, fromOpen, "errand %s left OPEN once", id)

		kinds := outboxKinds(t, pool, id)
		releases := countOf(kinds, errand.KindCreditRelease)
		if e.Status == errand.StatusAssigned {
			assert.Zero(t, releases)
			assert.Equal(t, 1, countOf(kinds, errand.ErrandAccepted.Kind()))
		} else {
			assert.Equal(t, 1, releases)
			assert.Zero(t, countOf(kinds, errand.ErrandAccepted.Kind()))
		}
	}
}

func TestOutboxAndStateCommitTogether(t *testing.T) {
	s, pool := newStore(t)
	ctx := context.Background()

	t.Run("a failing outbox insert rolls back the state change", func(t *testing.T) {
		snapshot := openErrand(t, s, newUUID(t))
		tr := next(t, snapshot, errand.EventCancel, requester(), errand.EventInput{}, t0)
		require.Len(t, tr.Outbox, 2)
		// Occupy the key of the transition's last row, so the first row and
		// the errand update succeed before the transaction fails.
		_, err := pool.Exec(ctx, `INSERT INTO outbox (kind, idempotency_key, errand_id, payload) VALUES ($1, $2, $3, '{}')`,
			tr.Outbox[1].Kind, tr.Outbox[1].IdempotencyKey, snapshot.ID)
		require.NoError(t, err)
		transitionsBefore := countTransitions(t, pool, snapshot.ID)
		kindsBefore := outboxKinds(t, pool, snapshot.ID)

		_, err = s.Apply(ctx, tr)
		require.Error(t, err)
		var appErr *apperror.Error
		require.False(t, errors.As(err, &appErr), "a duplicate key is a bug, not a client error: %v", err)

		got, err := s.Get(ctx, snapshot.ID)
		require.NoError(t, err)
		assert.Equal(t, snapshot, got, "the errand is unchanged")
		assert.Equal(t, transitionsBefore, countTransitions(t, pool, snapshot.ID))
		assert.Equal(t, kindsBefore, outboxKinds(t, pool, snapshot.ID), "the release row was rolled back too")
	})

	t.Run("a committed transition has its outbox rows", func(t *testing.T) {
		snapshot := openErrand(t, s, newUUID(t))
		tr := next(t, snapshot, errand.EventCancel, requester(), errand.EventInput{}, t0)
		stored, err := s.Apply(ctx, tr)
		require.NoError(t, err)
		assert.Equal(t, errand.StatusCancelled, stored.Status)
		keys := outboxKeys(t, pool, snapshot.ID)
		for _, row := range tr.Outbox {
			assert.Contains(t, keys, row.IdempotencyKey)
		}

		var payload []byte
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT payload FROM outbox WHERE idempotency_key = $1`, snapshot.ID+":release",
		).Scan(&payload))
		var cmd errand.CreditCommand
		require.NoError(t, json.Unmarshal(payload, &cmd))
		assert.Equal(t, errand.CreditCommand{ErrandID: snapshot.ID, ErrandVersion: 3, RequesterID: requesterID}, cmd)
	})

	t.Run("a failing decide rolls back the whole ClaimDue batch", func(t *testing.T) {
		s, pool := newStore(t)
		first := openErrand(t, s, newUUID(t))
		openErrand(t, s, newUUID(t))
		now := t0.Add(2 * time.Hour)
		calls := 0
		_, err := s.ClaimDue(ctx, errand.AcceptanceExpiry, now, 10, func(e errand.Errand) (errand.Transition, bool, error) {
			calls++
			if calls == 2 {
				return errand.Transition{}, false, errors.New("settings unavailable")
			}
			return expire(now)(e)
		})
		require.ErrorContains(t, err, "settings unavailable")
		got, err := s.Get(ctx, first.ID)
		require.NoError(t, err)
		assert.Equal(t, errand.StatusOpen, got.Status)
		assert.Zero(t, countOf(outboxKinds(t, pool, first.ID), errand.KindCreditRelease))
	})
}

func TestApplyFencesSupplierChangingEdits(t *testing.T) {
	s, pool := newStore(t)
	ctx := context.Background()
	edit := func(e errand.Errand, supplier string, reward int) errand.EventInput {
		d := e.Details
		d.SupplierID, d.Reward = supplier, reward
		return errand.EventInput{Edit: &d}
	}

	other := newUUID(t)
	insertFence(t, pool, other, "OPEN")
	e := openErrand(t, s, newUUID(t))
	_, err := s.Apply(ctx, next(t, e, errand.EventEdit, requester(), edit(e, other, e.Details.Reward), t0))
	requireCode(t, apperror.SupplierDeletionInProgress, err)
	_, err = s.Apply(ctx, next(t, e, errand.EventEdit, requester(), edit(e, other, e.Details.Reward+5), t0))
	requireCode(t, apperror.SupplierDeletionInProgress, err)
	got, err := s.Get(ctx, e.ID)
	require.NoError(t, err)
	assert.Equal(t, e, got)

	unfenced := newUUID(t)
	stored, err := s.Apply(ctx, next(t, e, errand.EventEdit, requester(), edit(e, unfenced, e.Details.Reward), t0))
	require.NoError(t, err)
	assert.Equal(t, unfenced, stored.Details.SupplierID)

	// An edit that keeps a fenced supplier is not checked.
	kept := newUUID(t)
	f := openErrand(t, s, kept)
	insertFence(t, pool, kept, "OPEN")
	_, err = s.Apply(ctx, next(t, f, errand.EventEdit, requester(), edit(f, kept, f.Details.Reward+5), t0))
	require.NoError(t, err)
}

func TestReviews(t *testing.T) {
	s, pool := newStore(t)
	ctx := context.Background()

	reported := pickedUp(t, s, "courier-a")
	_, err := s.Apply(ctx, next(t, reported, errand.EventReportIssue, courier("courier-a"),
		errand.EventInput{Reason: "shop closed"}, t0))
	require.NoError(t, err)
	r := openReviewOf(t, pool, reported.ID)
	assert.Equal(t, "COURIER_ISSUE", r.reason)
	assert.Equal(t, ptr("courier-a"), r.reporter)
	assert.Equal(t, ptr("shop closed"), r.note)

	inReview, err := s.Get(ctx, reported.ID)
	require.NoError(t, err)
	admin := errand.Actor{Type: errand.ActorAdmin, ID: "admin-1"}
	refunded, err := s.Apply(ctx, next(t, inReview, errand.EventAdminRefund, admin,
		errand.EventInput{Reason: "never picked up"}, t0.Add(time.Minute)))
	require.NoError(t, err)
	assert.Equal(t, errand.StatusRefunded, refunded.Status)
	var resolution, resolver, resolutionReason string
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT resolution, resolver_id, resolution_reason FROM errand_reviews WHERE errand_id = $1`, reported.ID,
	).Scan(&resolution, &resolver, &resolutionReason))
	assert.Equal(t, []string{"REFUNDED", "admin-1", "never picked up"}, []string{resolution, resolver, resolutionReason})

	overdue := pickedUp(t, s, "courier-b")
	now := t0.Add(48 * time.Hour)
	n, err := s.ClaimDue(ctx, errand.DeliveryDeadline, now, 10, func(e errand.Errand) (errand.Transition, bool, error) {
		return next(t, e, errand.EventDeliveryDeadlinePassed, errand.SystemActor(), errand.EventInput{}, now), true, nil
	})
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	r = openReviewOf(t, pool, overdue.ID)
	assert.Equal(t, "DELIVERY_OVERDUE", r.reason)
	assert.Nil(t, r.reporter, "the system raised it")
	assert.Nil(t, r.note)

	// Resolving an errand with no open review is a bug and changes nothing.
	_, err = pool.Exec(ctx, `DELETE FROM errand_reviews WHERE errand_id = $1`, overdue.ID)
	require.NoError(t, err)
	stuck, err := s.Get(ctx, overdue.ID)
	require.NoError(t, err)
	_, err = s.Apply(ctx, next(t, stuck, errand.EventAdminComplete, admin, errand.EventInput{Reason: "fine"}, now))
	require.ErrorContains(t, err, "no open review")
	got, err := s.Get(ctx, overdue.ID)
	require.NoError(t, err)
	assert.Equal(t, errand.StatusReviewRequired, got.Status)
}

func TestRecordRejection(t *testing.T) {
	s, pool := newStore(t)
	ctx := context.Background()
	before := openErrand(t, s, newUUID(t))
	rowsBefore := len(outboxKinds(t, pool, before.ID))

	r := errand.Rejection{
		ErrandID:  before.ID,
		Action:    errand.RejectedAccept,
		Actor:     courier("courier-a"),
		Code:      apperror.ErrandChanged,
		RequestID: "req-1",
		At:        t0,
	}
	require.NoError(t, s.RecordRejection(ctx, r))
	require.NoError(t, s.RecordRejection(ctx, r), "a retried request is a no-op")
	assert.Len(t, outboxKinds(t, pool, before.ID), rowsBefore+1)

	var kind string
	var payload []byte
	require.NoError(t, pool.QueryRow(ctx, `SELECT kind, payload FROM outbox WHERE idempotency_key = $1`,
		before.ID+":accept-rejected:req-1").Scan(&kind, &payload))
	assert.Equal(t, "event.errand.accept_rejected", kind)
	var data errand.RejectionData
	require.NoError(t, json.Unmarshal(payload, &data))
	assert.Equal(t, before.Version, data.ErrandVersion)
	assert.Equal(t, errand.StatusOpen, data.Status)
	assert.Equal(t, "courier-a", data.ActorID)

	after, err := s.Get(ctx, before.ID)
	require.NoError(t, err)
	assert.Equal(t, before, after, "the errand is unchanged")

	r.Action = "withdraw"
	requireCode(t, apperror.InvalidArgument, s.RecordRejection(ctx, r))
	r.Action, r.ErrandID = errand.RejectedCancel, newUUID(t)
	requireCode(t, apperror.ErrandNotFound, s.RecordRejection(ctx, r))
}

func TestLists(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	supplier, otherSupplier := newUUID(t), newUUID(t)

	// Three open errands, submitted a minute apart, due one hour apart.
	var open []errand.Errand
	for i := range 3 {
		e := newErrand(t, supplier)
		if i == 2 {
			e.Details.SupplierID = otherSupplier
		}
		at := t0.Add(time.Duration(i) * time.Minute)
		e.Details.AcceptanceDeadline = t0.Add(time.Duration(i+1) * time.Hour)
		_, _, err := s.Create(ctx, e, submitTransition(t, e, at))
		require.NoError(t, err)
		created, err := s.Get(ctx, e.ID)
		require.NoError(t, err)
		e, err = s.Apply(ctx, next(t, created, errand.EventReservationConfirmed, errand.SystemActor(), errand.EventInput{}, at))
		require.NoError(t, err)
		open = append(open, e)
	}

	first, err := s.ListOpen(ctx, errand.OpenQuery{Now: t0, Limit: 2})
	require.NoError(t, err)
	assert.Equal(t, []string{open[0].ID, open[1].ID}, ids(first.Items), "soonest deadline first")
	require.NotNil(t, first.Next)
	second, err := s.ListOpen(ctx, errand.OpenQuery{Now: t0, Limit: 2, After: first.Next})
	require.NoError(t, err)
	assert.Equal(t, []string{open[2].ID}, ids(second.Items))
	assert.Nil(t, second.Next)

	later, err := s.ListOpen(ctx, errand.OpenQuery{Now: t0.Add(90 * time.Minute), Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, []string{open[1].ID, open[2].ID}, ids(later.Items), "a passed deadline is hidden")
	bySupplier, err := s.ListOpen(ctx, errand.OpenQuery{Now: t0, SupplierID: otherSupplier, Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, []string{open[2].ID}, ids(bySupplier.Items))

	_, err = s.Apply(ctx, next(t, open[0], errand.EventAccept, courier("courier-a"), acceptInput(), t0.Add(5*time.Minute)))
	require.NoError(t, err)
	_, err = s.Apply(ctx, next(t, open[1], errand.EventCancel, requester(), errand.EventInput{}, t0.Add(6*time.Minute)))
	require.NoError(t, err)

	requested, err := s.ListRequested(ctx, errand.UserListQuery{UserID: requesterID, Limit: 1})
	require.NoError(t, err)
	assert.Equal(t, []string{open[2].ID}, ids(requested.Items), "newest first, active only")
	require.NotNil(t, requested.Next)
	requested, err = s.ListRequested(ctx, errand.UserListQuery{UserID: requesterID, Limit: 1, After: requested.Next})
	require.NoError(t, err)
	assert.Equal(t, []string{open[0].ID}, ids(requested.Items))
	assert.Nil(t, requested.Next)

	delivering, err := s.ListDelivering(ctx, errand.UserListQuery{UserID: "courier-a", Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, []string{open[0].ID}, ids(delivering.Items))

	history, err := s.ListHistory(ctx, errand.UserListQuery{UserID: requesterID, Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, []string{open[1].ID}, ids(history.Items))
	courierHistory, err := s.ListHistory(ctx, errand.UserListQuery{UserID: "courier-a", Limit: 10})
	require.NoError(t, err)
	assert.Empty(t, courierHistory.Items)

	_, err = s.ListHistory(ctx, errand.UserListQuery{UserID: requesterID, Limit: 0})
	requireCode(t, apperror.InvalidArgument, err)
	_, err = s.ListOpen(ctx, errand.OpenQuery{Now: t0, Limit: errand.MaxListLimit + 1})
	requireCode(t, apperror.InvalidArgument, err)
}

func TestCourierHistory(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()

	withdrawn := openErrand(t, s, newUUID(t))
	accepted, err := s.Apply(ctx, next(t, withdrawn, errand.EventAccept, courier("courier-a"), acceptInput(), t0))
	require.NoError(t, err)
	_, err = s.Apply(ctx, next(t, accepted, errand.EventWithdraw, courier("courier-a"), errand.EventInput{}, t0))
	require.NoError(t, err)

	forced := openErrand(t, s, newUUID(t))
	_, err = s.Apply(ctx, next(t, forced, errand.EventAccept, courier("courier-a"), acceptInput(), t0))
	require.NoError(t, err)
	forcedAt := t0.Add(48 * time.Hour)
	n, err := s.ClaimDue(ctx, errand.PickupDeadline, forcedAt, 10, func(e errand.Errand) (errand.Transition, bool, error) {
		return next(t, e, errand.EventPickupDeadlinePassed, errand.SystemActor(), errand.EventInput{}, forcedAt), true, nil
	})
	require.NoError(t, err)
	require.Equal(t, 1, n)

	h, err := s.CourierHistory(ctx, withdrawn.ID, "courier-a", t0.Add(-time.Minute))
	require.NoError(t, err)
	assert.Equal(t, errand.CourierHistory{WithdrewFromErrand: true, WithdrawalsSince: 2}, h,
		"the forced withdrawal is attributed to the courier")

	h, err = s.CourierHistory(ctx, newUUID(t), "courier-a", t0.Add(time.Hour))
	require.NoError(t, err)
	assert.Equal(t, errand.CourierHistory{WithdrawalsSince: 1}, h)

	h, err = s.CourierHistory(ctx, withdrawn.ID, "courier-b", t0.Add(-time.Minute))
	require.NoError(t, err)
	assert.Zero(t, h)
}

func TestClaimDueSkipsLockedErrands(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	for range 4 {
		openErrand(t, s, newUUID(t))
	}
	now := t0.Add(2 * time.Hour)

	holding := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	var firstBatch []string
	go func() {
		_, err := s.ClaimDue(ctx, errand.AcceptanceExpiry, now, 2, func(e errand.Errand) (errand.Transition, bool, error) {
			firstBatch = append(firstBatch, e.ID)
			if len(firstBatch) == 2 {
				close(holding)
				<-release
			}
			return expire(now)(e)
		})
		done <- err
	}()
	<-holding

	var secondBatch []string
	n, err := s.ClaimDue(ctx, errand.AcceptanceExpiry, now, 10, func(e errand.Errand) (errand.Transition, bool, error) {
		secondBatch = append(secondBatch, e.ID)
		return expire(now)(e)
	})
	require.NoError(t, err)
	assert.Equal(t, 2, n, "the locked errands are skipped, not waited for")
	close(release)
	require.NoError(t, <-done)
	for _, id := range secondBatch {
		assert.NotContains(t, firstBatch, id)
	}

	n, err = s.ClaimDue(ctx, errand.AcceptanceExpiry, now, 10, expire(now))
	require.NoError(t, err)
	assert.Zero(t, n)

	_, err = s.ClaimDue(ctx, "someday", now, 10, expire(now))
	requireCode(t, apperror.InvalidArgument, err)
}

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

const requesterID = "requester-1"

var t0 = time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)

func newErrand(t *testing.T, supplierID string) errand.Errand {
	t.Helper()
	return errand.Errand{
		ID:          newUUID(t),
		RequesterID: requesterID,
		Details: errand.Details{
			SupplierID:               supplierID,
			SupplierVersionID:        newUUID(t),
			DeliveryBuildingID:       newUUID(t),
			DeliveryBuildingSnapshot: json.RawMessage(`{"name": "COM1"}`),
			DeliveryDetail:           "Level 2 lounge",
			Items:                    json.RawMessage(`[{"description": "iced latte"}]`),
			ContactName:              "Alex Tan",
			ContactMobile:            "+65 9123 4567",
			Reward:                   10,
			AcceptanceDeadline:       t0.Add(time.Hour),
		},
		CreateIdempotencyKey: newUUID(t),
	}
}

func requester() errand.Actor { return errand.Actor{Type: errand.ActorRequester, ID: requesterID} }

func courier(id string) errand.Actor { return errand.Actor{Type: errand.ActorCourier, ID: id} }

func acceptInput() errand.EventInput {
	return errand.EventInput{Deadlines: errand.DefaultDeadlineSettings()}
}

func next(t *testing.T, e errand.Errand, ev errand.Event, actor errand.Actor, input errand.EventInput, now time.Time) errand.Transition {
	t.Helper()
	tr, err := errand.Next(e, ev, actor, input, now)
	require.NoError(t, err)
	return tr
}

func submitTransition(t *testing.T, e errand.Errand, now time.Time) errand.Transition {
	t.Helper()
	return next(t, e, errand.EventSubmit, errand.Actor{Type: errand.ActorRequester, ID: e.RequesterID}, errand.EventInput{}, now)
}

// expire is a decide callback for AcceptanceExpiry.
func expire(now time.Time) errand.DecideFunc {
	return func(e errand.Errand) (errand.Transition, bool, error) {
		tr, err := errand.Next(e, errand.EventAcceptanceDeadlinePassed, errand.SystemActor(), errand.EventInput{}, now)
		if err != nil {
			return errand.Transition{}, false, nil
		}
		return tr, true, nil
	}
}

// openErrand creates an errand at t0 and confirms its reservation.
func openErrand(t *testing.T, s *errandrepo.Store, supplierID string) errand.Errand {
	t.Helper()
	ctx := context.Background()
	e := newErrand(t, supplierID)
	created, _, err := s.Create(ctx, e, submitTransition(t, e, t0))
	require.NoError(t, err)
	open, err := s.Apply(ctx, next(t, created, errand.EventReservationConfirmed, errand.SystemActor(), errand.EventInput{}, t0))
	require.NoError(t, err)
	require.Equal(t, errand.StatusOpen, open.Status)
	return open
}

// pickedUp opens an errand, and courierID accepts and picks it up at t0.
func pickedUp(t *testing.T, s *errandrepo.Store, courierID string) errand.Errand {
	t.Helper()
	ctx := context.Background()
	e, err := s.Apply(ctx, next(t, openErrand(t, s, newUUID(t)), errand.EventAccept, courier(courierID), acceptInput(), t0))
	require.NoError(t, err)
	e, err = s.Apply(ctx, next(t, e, errand.EventPickUp, courier(courierID), errand.EventInput{}, t0))
	require.NoError(t, err)
	return e
}

type review struct {
	reason   string
	reporter *string
	note     *string
}

func openReviewOf(t *testing.T, pool *pgxpool.Pool, errandID string) review {
	t.Helper()
	var r review
	require.NoError(t, pool.QueryRow(context.Background(), `
		SELECT reason, reporter_id, note FROM errand_reviews
		WHERE errand_id = $1 AND resolution IS NULL`, errandID,
	).Scan(&r.reason, &r.reporter, &r.note))
	return r
}

func insertFence(t *testing.T, pool *pgxpool.Pool, supplierID, state string) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO supplier_fences (operation_id, supplier_id, state) VALUES (gen_random_uuid(), $1, $2)`,
		supplierID, state)
	require.NoError(t, err)
}

func outboxKinds(t *testing.T, pool *pgxpool.Pool, errandID string) []string {
	return outboxColumn(t, pool, "kind", errandID)
}

func outboxKeys(t *testing.T, pool *pgxpool.Pool, errandID string) []string {
	return outboxColumn(t, pool, "idempotency_key", errandID)
}

func outboxColumn(t *testing.T, pool *pgxpool.Pool, column, errandID string) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT `+column+` FROM outbox WHERE errand_id = $1 ORDER BY id`, errandID)
	require.NoError(t, err)
	values, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	return values
}

func countTransitions(t *testing.T, pool *pgxpool.Pool, errandID string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM errand_transitions WHERE errand_id = $1`, errandID).Scan(&n))
	return n
}

func countOf(values []string, want string) int {
	n := 0
	for _, v := range values {
		if v == want {
			n++
		}
	}
	return n
}

func ids(items []errand.Errand) []string {
	out := []string{}
	for _, e := range items {
		out = append(out, e.ID)
	}
	return out
}

func ptr[T any](v T) *T { return &v }

func requireCode(t *testing.T, want apperror.Code, err error) {
	t.Helper()
	require.Error(t, err)
	var appErr *apperror.Error
	require.True(t, errors.As(err, &appErr), "want *apperror.Error, got %T: %v", err, err)
	require.Equal(t, want, appErr.Code, "error: %v", err)
}

// newStore returns a store on a pool whose connections use a new schema with
// every migration applied. The pool is also used for fixtures and checks.
func newStore(t *testing.T) (*errandrepo.Store, *pgxpool.Pool) {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set; skipping PostgreSQL errand store integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	admin, err := pgx.Connect(ctx, databaseURL)
	require.NoError(t, err)
	schema := "order_errandrepo_" + strings.ReplaceAll(newUUID(t), "-", "")
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	_, err = admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema)
	require.NoError(t, err)

	config, err := pgxpool.ParseConfig(databaseURL)
	require.NoError(t, err)
	config.ConnConfig.RuntimeParams["search_path"] = schema
	config.MaxConns = 16
	pool, err := pgxpool.NewWithConfig(ctx, config)
	require.NoError(t, err)
	t.Cleanup(func() {
		pool.Close()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		_, _ = admin.Exec(cleanupCtx, "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
		_ = admin.Close(cleanupCtx)
	})

	up, err := migrations.Up()
	require.NoError(t, err)
	_, err = pool.Exec(ctx, up)
	require.NoError(t, err)
	return errandrepo.New(pool), pool
}

func newUUID(t *testing.T) string {
	t.Helper()
	value := make([]byte, 16)
	_, err := rand.Read(value)
	require.NoError(t, err)
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}
