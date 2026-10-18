package scheduler_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/scheduler"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/testfakes"
)

// startRun runs h's scheduler until the test ends.
func startRun(t *testing.T, h *harness) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.sched.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Run did not return after cancel")
		}
	})
}

// longIntervals keeps every job on its first, immediate run.
func longIntervals() scheduler.Config {
	return scheduler.Config{
		AcceptanceExpiryInterval:  time.Hour,
		PickupDeadlineInterval:    time.Hour,
		DeliveryDeadlineInterval:  time.Hour,
		VerificationLapseInterval: time.Hour,
		BatchSize:                 2,
	}
}

func TestRunDrainsFullBatchesAtOnce(t *testing.T) {
	h := newHarness(t, longIntervals())
	for i := range 5 {
		h.store.Put(openErrand(fmt.Sprintf("e%d", i), t0.Add(-time.Minute)))
	}

	startRun(t, h)

	// Batches of 2, 2 and 1 on the first run, without waiting an interval.
	require.Eventually(t, func() bool {
		return h.sched.Stats()[scheduler.JobAcceptanceExpiry].Applied == 5
	}, 2*time.Second, 5*time.Millisecond)
	for i := range 5 {
		assert.Equal(t, errand.StatusExpired, h.status(t, fmt.Sprintf("e%d", i)))
	}
	assert.Equal(t, uint64(3), h.sched.Stats()[scheduler.JobAcceptanceExpiry].Batches)
}

func TestRunStopsDrainingWhenAFullBatchAppliesNothing(t *testing.T) {
	var calls atomic.Int64
	h := newHarnessOver(t, longIntervals(), func(s *testfakes.FakeStore) errand.Store {
		return &claimHook{FakeStore: s, claimDue: func(ctx context.Context, kind errand.DueKind, now time.Time, limit int, decide errand.DecideFunc) (int, error) {
			if kind != errand.AcceptanceExpiry {
				return s.ClaimDue(ctx, kind, now, limit, decide)
			}
			calls.Add(1)
			// Every errand decide sees has had its deadline extended.
			return s.ClaimDue(ctx, kind, now, limit, func(e errand.Errand) (errand.Transition, bool, error) {
				e.Details.AcceptanceDeadline = now.Add(time.Hour)
				return decide(e)
			})
		}}
	})
	for i := range 4 {
		h.store.Put(openErrand(fmt.Sprintf("e%d", i), t0.Add(-time.Minute)))
	}

	startRun(t, h)

	require.Eventually(t, func() bool { return calls.Load() >= 1 }, 2*time.Second, 5*time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, int64(1), calls.Load(), "a full batch of skips must not be re-run at once")
	assert.Equal(t, scheduler.JobStats{Batches: 1, Seen: 2, Skipped: 2}, h.sched.Stats()[scheduler.JobAcceptanceExpiry])
}

func TestRunKeepsTickingAndIsolatesPanics(t *testing.T) {
	var mu sync.Mutex
	claims := map[errand.DueKind]int{}
	h := newHarnessOver(t, scheduler.Config{
		AcceptanceExpiryInterval:  5 * time.Millisecond,
		PickupDeadlineInterval:    5 * time.Millisecond,
		DeliveryDeadlineInterval:  5 * time.Millisecond,
		VerificationLapseInterval: 5 * time.Millisecond,
	}, func(s *testfakes.FakeStore) errand.Store {
		return &claimHook{FakeStore: s, claimDue: func(ctx context.Context, kind errand.DueKind, now time.Time, limit int, decide errand.DecideFunc) (int, error) {
			mu.Lock()
			claims[kind]++
			mu.Unlock()
			if kind == errand.AcceptanceExpiry {
				panic("acceptance expiry is broken")
			}
			return s.ClaimDue(ctx, kind, now, limit, decide)
		}}
	})
	h.store.Put(openErrand("open", t0.Add(-time.Minute)))
	h.store.Put(deliveredErrand("delivered", t0.Add(-time.Minute)))

	startRun(t, h)

	// The verification job keeps working and the panicking job keeps being
	// retried on later ticks.
	require.Eventually(t, func() bool {
		return h.sched.Stats()[scheduler.JobAcceptanceExpiry].Panics >= 3 &&
			statusOf(h, "delivered") == errand.StatusCompleted
	}, 2*time.Second, 5*time.Millisecond)
	// A later deliverable errand is still picked up by the healthy jobs.
	h.store.Put(pickedUpErrand("overdue", t0.Add(-time.Minute)))
	require.Eventually(t, func() bool {
		return statusOf(h, "overdue") == errand.StatusReviewRequired
	}, 2*time.Second, 5*time.Millisecond)

	assert.Equal(t, errand.StatusOpen, h.status(t, "open"))
	mu.Lock()
	assert.Greater(t, claims[errand.PickupDeadline], 1, "jobs tick repeatedly")
	mu.Unlock()
	assert.Contains(t, h.logs.String(), `"msg":"scheduler job panicked"`)
	assert.Contains(t, h.logs.String(), "acceptance expiry is broken")
}

func TestRunReturnsWhenCancelled(t *testing.T) {
	h := newHarness(t, scheduler.Config{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.sched.Run(ctx)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestJobsAreTheDeadlineJobsWithoutReminderDependencies(t *testing.T) {
	h := newHarness(t, scheduler.Config{})
	assert.Equal(t, []string{
		scheduler.JobAcceptanceExpiry, scheduler.JobPickupDeadline,
		scheduler.JobDeliveryDeadline, scheduler.JobVerificationLapse,
	}, h.sched.Jobs())
}

func TestJobsIncludeRemindersWhenConfigured(t *testing.T) {
	h := newReminderHarness(t)
	assert.Equal(t, []string{
		scheduler.JobAcceptanceExpiry, scheduler.JobPickupDeadline,
		scheduler.JobDeliveryDeadline, scheduler.JobVerificationLapse,
		scheduler.JobVerificationReminder, scheduler.JobPickupReminder,
	}, h.sched.Jobs())
}

func TestNewRejectsRemindersWithoutSettings(t *testing.T) {
	assert.Panics(t, func() {
		scheduler.New(testfakes.NewFakeStore(), testfakes.NewFakeClock(t0), scheduler.Config{
			Reminders: newFakeReminders(testfakes.NewFakeStore()),
		})
	})
}

// statusOf is harness.status for Eventually conditions, which run outside
// the test goroutine.
func statusOf(h *harness, id string) errand.Status {
	e, _ := h.store.Errand(id)
	return e.Status
}
