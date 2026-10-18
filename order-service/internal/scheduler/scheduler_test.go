package scheduler_test

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/scheduler"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/testfakes"
)

func TestAcceptanceExpiryExpiresOnlyDueErrands(t *testing.T) {
	h := newHarness(t, scheduler.Config{})
	h.store.Put(openErrand("past", t0.Add(-time.Minute)))
	h.store.Put(openErrand("exact", t0)) // a deadline at now has passed
	h.store.Put(openErrand("future", t0.Add(time.Second)))
	h.store.Put(assignedErrand("assigned", t0.Add(-time.Minute), t0.Add(time.Hour)))

	res := h.runOnce(t, errand.AcceptanceExpiry)

	assert.Equal(t, scheduler.BatchResult{Seen: 2, Applied: 2}, res)
	assert.Equal(t, errand.StatusExpired, h.status(t, "past"))
	assert.Equal(t, errand.StatusExpired, h.status(t, "exact"))
	assert.Equal(t, errand.StatusOpen, h.status(t, "future"))
	assert.Equal(t, errand.StatusAssigned, h.status(t, "assigned"))
	for _, id := range []string{"past", "exact"} {
		assert.Equal(t, []string{errand.KindCreditRelease, errand.ErrandExpired.Kind()}, h.outboxFor(id), id)
	}
	assert.Empty(t, h.outboxFor("future"))

	for _, tr := range h.store.Transitions() {
		assert.Equal(t, errand.SystemActor(), tr.Actor)
		assert.Equal(t, errand.EventAcceptanceDeadlinePassed, tr.Event)
	}
}

func TestPickupDeadlineWithdrawsTheCourier(t *testing.T) {
	h := newHarness(t, scheduler.Config{})
	// Before the acceptance deadline the errand goes back to OPEN.
	h.store.Put(assignedErrand("reopen", t0.Add(time.Hour), t0.Add(-time.Minute)))
	// After it the errand expires and the reservation is released.
	h.store.Put(assignedErrand("expire", t0.Add(-time.Minute), t0.Add(-time.Minute)))
	h.store.Put(assignedErrand("not-due", t0.Add(time.Hour), t0.Add(time.Minute)))

	res := h.runOnce(t, errand.PickupDeadline)

	assert.Equal(t, scheduler.BatchResult{Seen: 2, Applied: 2}, res)

	reopened, _ := h.store.Errand("reopen")
	assert.Equal(t, errand.StatusOpen, reopened.Status)
	assert.Empty(t, reopened.CourierID)
	assert.Nil(t, reopened.PickupDeadline)
	assert.Nil(t, reopened.DeliveryDeadline)
	assert.Equal(t, []string{errand.ErrandWithdrawn.Kind()}, h.outboxFor("reopen"))

	assert.Equal(t, errand.StatusExpired, h.status(t, "expire"))
	assert.Equal(t,
		[]string{errand.KindCreditRelease, errand.ErrandWithdrawn.Kind(), errand.ErrandExpired.Kind()},
		h.outboxFor("expire"))

	assert.Equal(t, errand.StatusAssigned, h.status(t, "not-due"))

	// Forced withdrawals count towards the courier's restriction (F3.6.4).
	hist, err := h.store.CourierHistory(context.Background(), "reopen", courierID, t0.Add(-15*time.Minute))
	require.NoError(t, err)
	assert.True(t, hist.WithdrewFromErrand)
	assert.Equal(t, 2, hist.WithdrawalsSince)
}

func TestDeliveryDeadlineOpensAnOverdueReview(t *testing.T) {
	h := newHarness(t, scheduler.Config{})
	h.store.Put(pickedUpErrand("overdue", t0.Add(-time.Minute)))
	h.store.Put(pickedUpErrand("on-time", t0.Add(time.Minute)))

	res := h.runOnce(t, errand.DeliveryDeadline)

	assert.Equal(t, scheduler.BatchResult{Seen: 1, Applied: 1}, res)
	overdue, _ := h.store.Errand("overdue")
	assert.Equal(t, errand.StatusReviewRequired, overdue.Status)
	assert.Equal(t, errand.ReviewDeliveryOverdue, overdue.ReviewReason)
	assert.Equal(t, courierID, overdue.CourierID, "the courier stays assigned while under review")
	assert.Equal(t, []string{errand.ErrandReviewRequired.Kind()}, h.outboxFor("overdue"), "no credit effect")
	assert.Equal(t, errand.StatusPickedUp, h.status(t, "on-time"))

	reviews := h.store.Reviews()
	require.Len(t, reviews, 1)
	assert.Equal(t, "overdue", reviews[0].ErrandID)
	assert.Equal(t, errand.ReviewDeliveryOverdue, reviews[0].Reason)
	assert.Empty(t, reviews[0].ReporterID, "a system review has no reporter")
	assert.Empty(t, reviews[0].Resolution)
	assert.Equal(t, t0, reviews[0].CreatedAt)
}

func TestVerificationLapseCompletesAndTransfers(t *testing.T) {
	h := newHarness(t, scheduler.Config{})
	h.store.Put(deliveredErrand("lapsed", t0))
	h.store.Put(deliveredErrand("waiting", t0.Add(time.Minute)))

	res := h.runOnce(t, errand.VerificationLapse)

	assert.Equal(t, scheduler.BatchResult{Seen: 1, Applied: 1}, res)
	assert.Equal(t, errand.StatusCompleted, h.status(t, "lapsed"))
	assert.Equal(t, errand.StatusDelivered, h.status(t, "waiting"))

	transfers := h.store.OutboxKinds(errand.KindCreditTransfer)
	require.Len(t, transfers, 1)
	assert.Equal(t, "lapsed:transfer", transfers[0].IdempotencyKey)
	cmd, ok := transfers[0].Payload.(errand.CreditCommand)
	require.True(t, ok)
	assert.Equal(t, courierID, cmd.CourierID)
	assert.Equal(t, requesterID, cmd.RequesterID)
	assert.Equal(t, reward, cmd.Amount)
	assert.Equal(t, []string{errand.KindCreditTransfer, errand.ErrandCompleted.Kind()}, h.outboxFor("lapsed"))
}

// TestSkipsAnErrandWhoseDeadlineWasExtended simulates an acceptance deadline
// extended between the errand's selection and decide: the errand decide
// receives is the row as locked, and its deadline is no longer due.
func TestSkipsAnErrandWhoseDeadlineWasExtended(t *testing.T) {
	h := newHarnessOver(t, scheduler.Config{}, func(s *testfakes.FakeStore) errand.Store {
		return &claimHook{FakeStore: s, claimDue: func(ctx context.Context, kind errand.DueKind, now time.Time, limit int, decide errand.DecideFunc) (int, error) {
			return s.ClaimDue(ctx, kind, now, limit, func(e errand.Errand) (errand.Transition, bool, error) {
				if e.ID == "extended" {
					e.Details.AcceptanceDeadline = now.Add(30 * time.Minute)
				}
				return decide(e)
			})
		}}
	})
	h.store.Put(openErrand("extended", t0.Add(-time.Minute)))
	h.store.Put(openErrand("due", t0.Add(-time.Minute)))

	res := h.runOnce(t, errand.AcceptanceExpiry)

	assert.Equal(t, scheduler.BatchResult{Seen: 2, Applied: 1}, res)
	assert.Equal(t, errand.StatusOpen, h.status(t, "extended"))
	assert.Empty(t, h.outboxFor("extended"))
	assert.Equal(t, errand.StatusExpired, h.status(t, "due"))

	stats := h.sched.Stats()[errand.AcceptanceExpiry]
	assert.Equal(t, scheduler.JobStats{Batches: 1, Seen: 2, Applied: 1, Skipped: 1}, stats)
	assert.Contains(t, h.logs.String(), "no longer due")
}

func TestSkipsAnErrandInAnotherState(t *testing.T) {
	// The errand decide receives changed state after selection.
	h := newHarnessOver(t, scheduler.Config{}, func(s *testfakes.FakeStore) errand.Store {
		return &claimHook{FakeStore: s, claimDue: func(ctx context.Context, kind errand.DueKind, now time.Time, limit int, decide errand.DecideFunc) (int, error) {
			return s.ClaimDue(ctx, kind, now, limit, func(e errand.Errand) (errand.Transition, bool, error) {
				e.Status = errand.StatusCancelled
				return decide(e)
			})
		}}
	})
	h.store.Put(openErrand("e1", t0.Add(-time.Minute)))

	res := h.runOnce(t, errand.AcceptanceExpiry)

	assert.Equal(t, scheduler.BatchResult{Seen: 1, Applied: 0}, res)
	assert.Equal(t, errand.StatusOpen, h.status(t, "e1"))
}

func TestClaimDueErrorIsReportedAndCounted(t *testing.T) {
	h := newHarness(t, scheduler.Config{})
	h.store.Put(openErrand("e1", t0.Add(-time.Minute)))
	boom := errors.New("database unavailable")
	h.store.FailNext("ClaimDue", boom)

	_, err := h.sched.RunOnce(context.Background(), errand.AcceptanceExpiry)

	require.ErrorIs(t, err, boom)
	assert.Equal(t, errand.StatusOpen, h.status(t, "e1"))
	assert.Equal(t, scheduler.JobStats{Batches: 1, Failures: 1}, h.sched.Stats()[errand.AcceptanceExpiry])
	assert.Contains(t, h.logs.String(), "scheduler batch failed")

	// The next run succeeds.
	assert.Equal(t, scheduler.BatchResult{Seen: 1, Applied: 1}, h.runOnce(t, errand.AcceptanceExpiry))
}

func TestRunOnceRejectsAnUnknownKind(t *testing.T) {
	h := newHarness(t, scheduler.Config{})
	_, err := h.sched.RunOnce(context.Background(), errand.StuckReservation)
	require.Error(t, err)
}

func TestLogsOneLinePerTransitionWithoutPersonalData(t *testing.T) {
	h := newHarness(t, scheduler.Config{})
	h.store.Put(deliveredErrand("e1", t0.Add(-time.Minute)))

	h.runOnce(t, errand.VerificationLapse)

	logs := h.logs.String()
	assert.Contains(t, logs, `"msg":"scheduler applied transition"`)
	assert.Contains(t, logs, `"job":"verification_lapse"`)
	assert.Contains(t, logs, `"errand_id":"e1"`)
	assert.Contains(t, logs, `"from":"DELIVERED"`)
	assert.Contains(t, logs, `"to":"COMPLETED"`)
	for _, personal := range []string{requesterID, courierID, contactName, contactMobile} {
		assert.NotContains(t, logs, personal)
	}
}

// TestAcceptRacesExpiry runs a courier's accept, decided just before the
// acceptance deadline, against the expiry job at the deadline. Whatever the
// interleaving, exactly one of them wins.
func TestAcceptRacesExpiry(t *testing.T) {
	const rounds = 200
	ctx := context.Background()
	wins := map[errand.Status]int{}
	for i := range rounds {
		h := newHarness(t, scheduler.Config{})
		h.store.Put(openErrand("e1", t0))

		var (
			wg        sync.WaitGroup
			start     = make(chan struct{})
			acceptErr error
			res       scheduler.BatchResult
			jobErr    error
		)
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			courier := errand.Actor{Type: errand.ActorCourier, ID: courierID}
			input := errand.EventInput{Deadlines: errand.DefaultDeadlineSettings()}
			_, _, acceptErr = h.store.Fire(ctx, "e1", errand.EventAccept, courier, input, t0.Add(-time.Millisecond))
		}()
		go func() {
			defer wg.Done()
			<-start
			if i%2 == 0 {
				// Let the accept get ahead on half the rounds, so both
				// interleavings are exercised.
				for range 3 {
					runtime.Gosched()
				}
			}
			res, jobErr = h.sched.RunOnce(ctx, errand.AcceptanceExpiry)
		}()
		close(start)
		wg.Wait()

		require.NoError(t, jobErr, "round %d", i)
		final := h.status(t, "e1")
		wins[final]++
		switch final {
		case errand.StatusAssigned:
			require.NoError(t, acceptErr, "round %d", i)
			require.Equal(t, 0, res.Applied, "round %d", i)
			require.Equal(t, []string{errand.ErrandAccepted.Kind()}, h.outboxFor("e1"), "round %d", i)
		case errand.StatusExpired:
			requireCode(t, apperror.ErrandUnavailable, acceptErr)
			require.Equal(t, 1, res.Applied, "round %d", i)
			require.Equal(t, []string{errand.KindCreditRelease, errand.ErrandExpired.Kind()}, h.outboxFor("e1"), "round %d", i)
		default:
			t.Fatalf("round %d: errand ended %s", i, final)
		}
		require.Len(t, h.store.Transitions(), 1, "round %d: exactly one transition", i)
	}
	t.Logf("outcomes over %d rounds: %v", rounds, wins)
}

func requireCode(t *testing.T, want apperror.Code, err error) {
	t.Helper()
	var appErr *apperror.Error
	require.True(t, errors.As(err, &appErr), "want *apperror.Error, got %T: %v", err, err)
	require.Equal(t, want, appErr.Code, "error: %v", err)
}
