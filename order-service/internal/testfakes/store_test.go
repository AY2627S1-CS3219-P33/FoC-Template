package testfakes_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/testfakes"
)

func TestFakeStoreCompareAndSwap(t *testing.T) {
	ctx := context.Background()

	t.Run("second accept from the same snapshot is ERRAND_UNAVAILABLE", func(t *testing.T) {
		s := testfakes.NewFakeStore()
		snapshot := openErrand(t, s, "e-1", t0)
		rowsBefore := len(s.Outbox())

		first, err := errand.Next(snapshot, errand.EventAccept, courier("courier-a"), acceptInput(), t0)
		require.NoError(t, err)
		second, err := errand.Next(snapshot, errand.EventAccept, courier("courier-b"), acceptInput(), t0)
		require.NoError(t, err)

		_, err = s.Apply(ctx, first)
		require.NoError(t, err)
		rowsAfterFirst := len(s.Outbox())
		require.Greater(t, rowsAfterFirst, rowsBefore)

		_, err = s.Apply(ctx, second)
		requireCode(t, apperror.ErrandUnavailable, err)

		stored, _ := s.Errand("e-1")
		assert.Equal(t, errand.StatusAssigned, stored.Status)
		assert.Equal(t, "courier-a", stored.CourierID)
		assert.Len(t, s.Outbox(), rowsAfterFirst, "a failed apply writes no outbox rows")
	})

	t.Run("stale version with the same status is ERRAND_CHANGED", func(t *testing.T) {
		s := testfakes.NewFakeStore()
		snapshot := openErrand(t, s, "e-1", t0)
		edit := snapshot.Details
		edit.Notes = "first"
		_, _, err := s.Fire(ctx, "e-1", errand.EventEdit, requester(), errand.EventInput{Edit: &edit}, t0)
		require.NoError(t, err)

		edit.Notes = "stale"
		stale, err := errand.Next(snapshot, errand.EventEdit, requester(), errand.EventInput{Edit: &edit}, t0)
		require.NoError(t, err)
		_, err = s.Apply(ctx, stale)
		requireCode(t, apperror.ErrandChanged, err)

		stored, _ := s.Errand("e-1")
		assert.Equal(t, "first", stored.Details.Notes)
	})

	t.Run("a pending reward edit is part of the compare", func(t *testing.T) {
		s := testfakes.NewFakeStore()
		snapshot := openErrand(t, s, "e-1", t0)
		edit := snapshot.Details
		edit.Reward = reward + 5
		_, _, err := s.Fire(ctx, "e-1", errand.EventEdit, requester(), errand.EventInput{Edit: &edit}, t0)
		require.NoError(t, err)

		// The version is unchanged, but the snapshot has no pending edit.
		cancel, err := errand.Next(snapshot, errand.EventCancel, requester(), errand.EventInput{}, t0)
		require.NoError(t, err)
		_, err = s.Apply(ctx, cancel)
		requireCode(t, apperror.ErrandChanged, err)
	})

	t.Run("unknown errand is ERRAND_NOT_FOUND", func(t *testing.T) {
		s := testfakes.NewFakeStore()
		snapshot := openErrand(t, s, "e-1", t0)
		tr, err := errand.Next(snapshot, errand.EventCancel, requester(), errand.EventInput{}, t0)
		require.NoError(t, err)
		tr.ErrandID = "missing"
		_, err = s.Apply(ctx, tr)
		requireCode(t, apperror.ErrandNotFound, err)
	})
}

func TestFakeStoreCreate(t *testing.T) {
	ctx := context.Background()
	s := testfakes.NewFakeStore()
	e := newErrand("e-1", t0)
	tr, err := errand.Next(e, errand.EventSubmit, requester(), errand.EventInput{}, t0)
	require.NoError(t, err)

	stored, created, err := s.Create(ctx, e, tr)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, errand.StatusPendingReservation, stored.Status)
	assert.Len(t, s.OutboxKinds(errand.KindCreditReserve), 1)

	// Same requester and key, different errand ID: the original comes back.
	replay := newErrand("e-2", t0)
	replay.CreateIdempotencyKey = e.CreateIdempotencyKey
	replayT, err := errand.Next(replay, errand.EventSubmit, requester(), errand.EventInput{}, t0)
	require.NoError(t, err)
	again, created, err := s.Create(ctx, replay, replayT)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, "e-1", again.ID)
	assert.Len(t, s.OutboxKinds(errand.KindCreditReserve), 1)

	s.Fence(supplierID)
	_, err = s.Submit(ctx, newErrand("e-3", t0), t0)
	requireCode(t, apperror.SupplierDeletionInProgress, err)
}

func TestFakeStoreApplyFencesSupplierChangingEdits(t *testing.T) {
	ctx := context.Background()
	const other = "supplier-2"
	edit := func(e errand.Errand, supplier string, reward int) errand.EventInput {
		d := e.Details
		d.SupplierID, d.Reward = supplier, reward
		return errand.EventInput{Edit: &d}
	}

	t.Run("an edit onto a fenced supplier is refused, applied or pending", func(t *testing.T) {
		s := testfakes.NewFakeStore()
		e := openErrand(t, s, "e-1", t0)
		s.Fence(other)
		rowsBefore := len(s.Outbox())

		_, _, err := s.Fire(ctx, "e-1", errand.EventEdit, requester(), edit(e, other, reward), t0)
		requireCode(t, apperror.SupplierDeletionInProgress, err)
		_, _, err = s.Fire(ctx, "e-1", errand.EventEdit, requester(), edit(e, other, reward+5), t0)
		requireCode(t, apperror.SupplierDeletionInProgress, err)

		stored, _ := s.Errand("e-1")
		assert.Equal(t, e, stored)
		assert.Len(t, s.Outbox(), rowsBefore)

		s.Unfence(other)
		stored, _, err = s.Fire(ctx, "e-1", errand.EventEdit, requester(), edit(e, other, reward), t0)
		require.NoError(t, err)
		assert.Equal(t, other, stored.Details.SupplierID)
	})

	t.Run("an edit that keeps a fenced supplier is applied", func(t *testing.T) {
		s := testfakes.NewFakeStore()
		e := openErrand(t, s, "e-1", t0)
		s.Fence(supplierID)

		_, _, err := s.Fire(ctx, "e-1", errand.EventEdit, requester(), edit(e, supplierID, reward+5), t0)
		require.NoError(t, err)
	})
}

func TestFakeStoreRecordRejection(t *testing.T) {
	ctx := context.Background()
	s := testfakes.NewFakeStore()
	before := openErrand(t, s, "e-1", t0)
	rowsBefore := len(s.Outbox())

	r := errand.Rejection{
		ErrandID:  "e-1",
		Action:    errand.RejectedAccept,
		Actor:     courier("courier-a"),
		Code:      apperror.ErrandChanged,
		RequestID: "req-1",
		At:        t0,
	}
	require.NoError(t, s.RecordRejection(ctx, r))
	require.NoError(t, s.RecordRejection(ctx, r), "a retried request is a no-op")

	rows := s.Outbox()[rowsBefore:]
	require.Len(t, rows, 1)
	assert.Equal(t, "event.errand.accept_rejected", rows[0].Kind)
	assert.Equal(t, "e-1:accept-rejected:req-1", rows[0].IdempotencyKey)
	data := rows[0].Payload.(errand.RejectionData)
	assert.Equal(t, before.Version, data.ErrandVersion)
	assert.Equal(t, errand.StatusOpen, data.Status)

	after, _ := s.Errand("e-1")
	assert.Equal(t, before, after, "the errand is unchanged")

	r.Action = "withdraw"
	requireCode(t, apperror.InvalidArgument, s.RecordRejection(ctx, r))
	r.Action, r.ErrandID = errand.RejectedCancel, "missing"
	requireCode(t, apperror.ErrandNotFound, s.RecordRejection(ctx, r))
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

func TestFakeStoreClaimDueNeverSharesAnErrand(t *testing.T) {
	ctx := context.Background()
	s := testfakes.NewFakeStore()
	ids := errandIDs(60)
	for _, id := range ids {
		openErrand(t, s, id, t0)
	}
	now := t0.Add(2 * time.Hour)

	var mu sync.Mutex
	decided := map[string]int{}
	decide := func(e errand.Errand) (errand.Transition, bool, error) {
		mu.Lock()
		decided[e.ID]++
		mu.Unlock()
		time.Sleep(time.Millisecond) // widen the window for overlap
		return expire(now)(e)
	}

	const workers = 8
	var wg sync.WaitGroup
	var total int
	var totalMu sync.Mutex
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				n, err := s.ClaimDue(ctx, errand.AcceptanceExpiry, now, 4, decide)
				if !assert.NoError(t, err) || n == 0 {
					return
				}
				totalMu.Lock()
				total += n
				totalMu.Unlock()
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, len(ids), total)
	for _, id := range ids {
		assert.Equal(t, 1, decided[id], "errand %s decided once", id)
		e, _ := s.Errand(id)
		assert.Equal(t, errand.StatusExpired, e.Status)
	}
	assert.Len(t, s.OutboxKinds(errand.KindCreditRelease), len(ids))
}

func TestFakeStoreClaimDue(t *testing.T) {
	ctx := context.Background()

	t.Run("selects only due errands, in deadline order, up to the limit", func(t *testing.T) {
		s := testfakes.NewFakeStore()
		openErrand(t, s, "late", t0.Add(30*time.Minute)) // deadline t0+90m
		openErrand(t, s, "early", t0)                    // deadline t0+60m
		openErrand(t, s, "not-due", t0.Add(2*time.Hour)) // deadline t0+3h
		now := t0.Add(2 * time.Hour)

		var seen []string
		n, err := s.ClaimDue(ctx, errand.AcceptanceExpiry, now, 10, func(e errand.Errand) (errand.Transition, bool, error) {
			seen = append(seen, e.ID)
			return expire(now)(e)
		})
		require.NoError(t, err)
		assert.Equal(t, 2, n)
		assert.Equal(t, []string{"early", "late"}, seen)
		e, _ := s.Errand("not-due")
		assert.Equal(t, errand.StatusOpen, e.Status)
	})

	t.Run("skip leaves the errand unchanged", func(t *testing.T) {
		s := testfakes.NewFakeStore()
		openErrand(t, s, "e-1", t0)
		n, err := s.ClaimDue(ctx, errand.AcceptanceExpiry, t0.Add(2*time.Hour), 10,
			func(errand.Errand) (errand.Transition, bool, error) { return errand.Transition{}, false, nil })
		require.NoError(t, err)
		assert.Zero(t, n)
		e, _ := s.Errand("e-1")
		assert.Equal(t, errand.StatusOpen, e.Status)
	})

	t.Run("a decide error rolls back the whole batch", func(t *testing.T) {
		s := testfakes.NewFakeStore()
		openErrand(t, s, "e-1", t0)
		openErrand(t, s, "e-2", t0.Add(time.Minute))
		now := t0.Add(2 * time.Hour)
		boom := errors.New("settings unavailable")
		calls := 0
		n, err := s.ClaimDue(ctx, errand.AcceptanceExpiry, now, 10, func(e errand.Errand) (errand.Transition, bool, error) {
			calls++
			if calls == 2 {
				return errand.Transition{}, false, boom
			}
			return expire(now)(e)
		})
		require.ErrorIs(t, err, boom)
		assert.Zero(t, n)
		for _, id := range []string{"e-1", "e-2"} {
			e, _ := s.Errand(id)
			assert.Equal(t, errand.StatusOpen, e.Status)
		}
		assert.Empty(t, s.OutboxKinds(errand.KindCreditRelease))
	})

	t.Run("an Apply on a claimed errand waits and then loses", func(t *testing.T) {
		s := testfakes.NewFakeStore()
		snapshot := openErrand(t, s, "e-1", t0)
		accept, err := errand.Next(snapshot, errand.EventAccept, courier("courier-a"), acceptInput(), t0.Add(59*time.Minute))
		require.NoError(t, err)
		now := t0.Add(time.Hour)

		inDecide, release := make(chan struct{}), make(chan struct{})
		claimDone := make(chan error, 1)
		go func() {
			_, err := s.ClaimDue(ctx, errand.AcceptanceExpiry, now, 10, func(e errand.Errand) (errand.Transition, bool, error) {
				close(inDecide)
				<-release
				return expire(now)(e)
			})
			claimDone <- err
		}()
		<-inDecide

		applyDone := make(chan error, 1)
		go func() {
			_, err := s.Apply(ctx, accept)
			applyDone <- err
		}()
		select {
		case <-applyDone:
			t.Fatal("Apply did not wait for the row lock")
		case <-time.After(20 * time.Millisecond):
		}

		close(release)
		require.NoError(t, <-claimDone)
		requireCode(t, apperror.ErrandUnavailable, <-applyDone)
		e, _ := s.Errand("e-1")
		assert.Equal(t, errand.StatusExpired, e.Status)
	})

	t.Run("stuck edit uses pending_edit_at as the cutoff", func(t *testing.T) {
		s := testfakes.NewFakeStore()
		snapshot := openErrand(t, s, "e-1", t0)
		edit := snapshot.Details
		edit.Reward = reward + 5
		_, _, err := s.Fire(ctx, "e-1", errand.EventEdit, requester(), errand.EventInput{Edit: &edit}, t0)
		require.NoError(t, err)

		count := func(cutoff time.Time) int {
			n := 0
			_, err := s.ClaimDue(ctx, errand.StuckEdit, cutoff, 10, func(errand.Errand) (errand.Transition, bool, error) {
				n++
				return errand.Transition{}, false, nil
			})
			require.NoError(t, err)
			return n
		}
		assert.Zero(t, count(t0.Add(-time.Second)))
		assert.Equal(t, 1, count(t0))
	})
}

func TestFakeStoreLists(t *testing.T) {
	ctx := context.Background()
	s := testfakes.NewFakeStore()
	for i, id := range errandIDs(5) {
		openErrand(t, s, id, t0.Add(time.Duration(i)*time.Minute))
	}
	_, _, err := s.Fire(ctx, "e-004", errand.EventAccept, courier("courier-a"), acceptInput(), t0.Add(5*time.Minute))
	require.NoError(t, err)

	var got []string
	q := errand.OpenQuery{Now: t0.Add(5 * time.Minute), Limit: 3}
	for {
		page, err := s.ListOpen(ctx, q)
		require.NoError(t, err)
		for _, e := range page.Items {
			got = append(got, e.ID)
		}
		if page.Next == nil {
			break
		}
		q.After = page.Next
	}
	assert.Equal(t, []string{"e-000", "e-001", "e-002", "e-003"}, got)

	// e-000's deadline (t0+1h) has passed: hidden.
	page, err := s.ListOpen(ctx, errand.OpenQuery{Now: t0.Add(time.Hour), Limit: 10})
	require.NoError(t, err)
	assert.Len(t, page.Items, 3)

	requested, err := s.ListRequested(ctx, errand.UserListQuery{UserID: requesterID, Limit: 10})
	require.NoError(t, err)
	require.Len(t, requested.Items, 5)
	assert.Equal(t, "e-004", requested.Items[0].ID, "newest first")

	delivering, err := s.ListDelivering(ctx, errand.UserListQuery{UserID: "courier-a", Limit: 10})
	require.NoError(t, err)
	require.Len(t, delivering.Items, 1)

	_, err = s.ListHistory(ctx, errand.UserListQuery{UserID: requesterID, Limit: 0})
	requireCode(t, apperror.InvalidArgument, err)
}

func TestFakeStoreCourierHistory(t *testing.T) {
	ctx := context.Background()
	s := testfakes.NewFakeStore()
	openErrand(t, s, "e-1", t0)
	openErrand(t, s, "e-2", t0)

	_, _, err := s.Fire(ctx, "e-1", errand.EventAccept, courier("courier-a"), acceptInput(), t0)
	require.NoError(t, err)
	_, _, err = s.Fire(ctx, "e-1", errand.EventWithdraw, courier("courier-a"), errand.EventInput{}, t0.Add(time.Minute))
	require.NoError(t, err)

	// A forced withdrawal: the pickup deadline passes while e-2 is assigned.
	settings := errand.DefaultDeadlineSettings()
	settings.PickupAfter = 10 * time.Minute
	_, _, err = s.Fire(ctx, "e-2", errand.EventAccept, courier("courier-a"), errand.EventInput{Deadlines: settings}, t0)
	require.NoError(t, err)
	_, _, err = s.Fire(ctx, "e-2", errand.EventPickupDeadlinePassed, errand.SystemActor(), errand.EventInput{}, t0.Add(10*time.Minute))
	require.NoError(t, err)

	h, err := s.CourierHistory(ctx, "e-1", "courier-a", t0)
	require.NoError(t, err)
	assert.Equal(t, errand.CourierHistory{WithdrewFromErrand: true, WithdrawalsSince: 2}, h)

	h, err = s.CourierHistory(ctx, "e-1", "courier-a", t0.Add(5*time.Minute))
	require.NoError(t, err)
	assert.Equal(t, 1, h.WithdrawalsSince)

	h, err = s.CourierHistory(ctx, "e-1", "courier-b", t0)
	require.NoError(t, err)
	assert.Equal(t, errand.CourierHistory{}, h)
}

func TestFakeStoreReviews(t *testing.T) {
	ctx := context.Background()
	admin := errand.Actor{Type: errand.ActorAdmin, ID: "admin-1"}
	pickedUp := func(t *testing.T, s *testfakes.FakeStore, id string) {
		t.Helper()
		openErrand(t, s, id, t0)
		_, _, err := s.Fire(ctx, id, errand.EventAccept, courier("courier-a"), acceptInput(), t0)
		require.NoError(t, err)
		_, _, err = s.Fire(ctx, id, errand.EventPickUp, courier("courier-a"), errand.EventInput{}, t0.Add(time.Minute))
		require.NoError(t, err)
	}

	t.Run("entering review opens a row and resolving it closes the row", func(t *testing.T) {
		s := testfakes.NewFakeStore()
		pickedUp(t, s, "e-1")
		reported := t0.Add(2 * time.Minute)
		_, _, err := s.Fire(ctx, "e-1", errand.EventReportIssue, courier("courier-a"), errand.EventInput{Reason: "shop closed"}, reported)
		require.NoError(t, err)
		require.Equal(t, []testfakes.ReviewRecord{{
			ErrandID: "e-1", Reason: errand.ReviewCourierIssue,
			ReporterID: "courier-a", Note: "shop closed", CreatedAt: reported,
		}}, s.Reviews())

		resolved := t0.Add(time.Hour)
		_, _, err = s.Fire(ctx, "e-1", errand.EventAdminRefund, admin, errand.EventInput{Reason: "confirmed with shop"}, resolved)
		require.NoError(t, err)
		reviews := s.Reviews()
		require.Len(t, reviews, 1)
		assert.Equal(t, errand.StatusRefunded, reviews[0].Resolution)
		assert.Equal(t, "admin-1", reviews[0].ResolverID)
		assert.Equal(t, "confirmed with shop", reviews[0].ResolutionReason)
		require.NotNil(t, reviews[0].ResolvedAt)
		assert.Equal(t, resolved, *reviews[0].ResolvedAt)
	})

	t.Run("a review raised by a deadline job has no reporter", func(t *testing.T) {
		s := testfakes.NewFakeStore()
		pickedUp(t, s, "e-1")
		now := t0.Add(3 * time.Hour)
		n, err := s.ClaimDue(ctx, errand.DeliveryDeadline, now, 10, func(e errand.Errand) (errand.Transition, bool, error) {
			tr, err := errand.Next(e, errand.EventDeliveryDeadlinePassed, errand.SystemActor(), errand.EventInput{}, now)
			return tr, err == nil, err
		})
		require.NoError(t, err)
		require.Equal(t, 1, n)
		assert.Equal(t, []testfakes.ReviewRecord{{
			ErrandID: "e-1", Reason: errand.ReviewDeliveryOverdue, CreatedAt: now,
		}}, s.Reviews())
	})

	t.Run("an errand put in review can be resolved", func(t *testing.T) {
		s := testfakes.NewFakeStore()
		e := newErrand("e-1", t0)
		e.Status, e.Version, e.CourierID = errand.StatusReviewRequired, 5, "courier-a"
		e.ReviewReason = errand.ReviewRequesterDispute
		s.Put(e)
		_, _, err := s.Fire(ctx, "e-1", errand.EventAdminComplete, admin, errand.EventInput{Reason: "photo shows delivery"}, t0)
		require.NoError(t, err)
		reviews := s.Reviews()
		require.Len(t, reviews, 1)
		assert.Equal(t, errand.StatusCompleted, reviews[0].Resolution)
	})
}

func TestFakeStoreFaults(t *testing.T) {
	ctx := context.Background()
	s := testfakes.NewFakeStore()
	openErrand(t, s, "e-1", t0)
	down := errors.New("database unavailable")
	s.FailNext("Get", down)
	_, err := s.Get(ctx, "e-1")
	require.ErrorIs(t, err, down)
	_, err = s.Get(ctx, "e-1")
	require.NoError(t, err)
}
