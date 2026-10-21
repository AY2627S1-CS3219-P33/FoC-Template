package scheduler_test

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/opsrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/scheduler"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/testfakes"
)

// fakeReminders is a scheduler.ReminderStore over a FakeStore, with the
// semantics of opsrepo's queries: candidates are read from the store's
// errands, and an insert is a no-op for a known key or an errand that left
// the selected status and version.
type fakeReminders struct {
	store *testfakes.FakeStore

	mu   sync.Mutex
	ids  []string
	rows []errand.OutboxRow
	keys map[string]bool
	// beforeInsert, when set, runs before each insert, outside the lock.
	beforeInsert func(errandID string)
}

var _ scheduler.ReminderStore = (*fakeReminders)(nil)

func newFakeReminders(store *testfakes.FakeStore) *fakeReminders {
	return &fakeReminders{store: store, keys: map[string]bool{}}
}

// track makes ListReminderCandidates consider errand id.
func (f *fakeReminders) track(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.Contains(f.ids, id) {
		f.ids = append(f.ids, id)
	}
}

func (f *fakeReminders) Rows() []errand.OutboxRow {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.rows)
}

func (f *fakeReminders) ListReminderCandidates(_ context.Context, kind opsrepo.ReminderKind, now, remindBefore time.Time, limit int) ([]opsrepo.ReminderCandidate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []opsrepo.ReminderCandidate
	for _, id := range f.ids {
		e, ok := f.store.Errand(id)
		if !ok {
			continue
		}
		var status errand.Status
		var deadline *time.Time
		var operation string
		switch kind {
		case opsrepo.ReminderVerification:
			status, deadline, operation = errand.StatusDelivered, e.VerificationDeadline, "verify-reminder"
		case opsrepo.ReminderPickup:
			status, deadline, operation = errand.StatusAssigned, e.PickupDeadline, "pickup-reminder"
		default:
			return nil, errors.New("unknown reminder kind")
		}
		if e.Status != status || deadline == nil || !deadline.After(now) || deadline.After(remindBefore) ||
			f.keys[errand.IdempotencyKey(e.ID, operation, e.Version)] {
			continue
		}
		out = append(out, opsrepo.ReminderCandidate{
			ErrandID: e.ID, Version: e.Version, Status: e.Status,
			RequesterID: e.RequesterID, CourierID: e.CourierID, Deadline: *deadline,
		})
	}
	slices.SortFunc(out, func(a, b opsrepo.ReminderCandidate) int {
		return cmp.Or(a.Deadline.Compare(b.Deadline), cmp.Compare(a.ErrandID, b.ErrandID))
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeReminders) InsertCurrentReminder(_ context.Context, row errand.OutboxRow, status errand.Status, version int) (bool, error) {
	if f.beforeInsert != nil {
		f.beforeInsert(row.ErrandID)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.store.Errand(row.ErrandID)
	if !ok || e.Status != status || e.Version != version || f.keys[row.IdempotencyKey] {
		return false, nil
	}
	f.keys[row.IdempotencyKey] = true
	f.rows = append(f.rows, row)
	return true, nil
}

type reminderHarness struct {
	*harness
	reminders *fakeReminders
	settings  *testfakes.FakeSettings
}

func newReminderHarness(t *testing.T) *reminderHarness {
	t.Helper()
	store := testfakes.NewFakeStore()
	h := &reminderHarness{
		harness:   &harness{store: store, clock: testfakes.NewFakeClock(t0), logs: &syncBuffer{}},
		reminders: newFakeReminders(store),
		settings:  testfakes.NewFakeSettings(),
	}
	h.sched = scheduler.New(store, h.clock, scheduler.Config{
		Reminders: h.reminders,
		Settings:  h.settings,
		Logger:    slog.New(slog.NewJSONHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	return h
}

func (h *reminderHarness) put(e errand.Errand) {
	h.store.Put(e)
	h.reminders.track(e.ID)
}

func (h *reminderHarness) keys() []string {
	var keys []string
	for _, r := range h.reminders.Rows() {
		keys = append(keys, r.IdempotencyKey)
	}
	return keys
}

// deliveredAt is a DELIVERED errand delivered at deliveredAt with the
// default 30 min verification period.
func deliveredAt(id string, at time.Time) errand.Errand {
	e := deliveredErrand(id, at.Add(30*time.Minute))
	e.DeliveredAt = ptr(at)
	return e
}

func TestVerificationReminderIsRaisedOnceAfterTheReminderDelay(t *testing.T) {
	h := newReminderHarness(t)
	h.put(deliveredAt("e1", t0))
	h.put(deliveredAt("lapsed", t0.Add(-time.Hour))) // auto-complete's business

	h.clock.Set(t0.Add(4*time.Minute + 59*time.Second))
	assert.Equal(t, scheduler.BatchResult{}, h.runOnce(t, scheduler.JobVerificationReminder))
	assert.Empty(t, h.reminders.Rows())

	// 5 min after delivery the verification deadline is 25 min away.
	h.clock.Set(t0.Add(5 * time.Minute))
	assert.Equal(t, scheduler.BatchResult{Seen: 1, Applied: 1}, h.runOnce(t, scheduler.JobVerificationReminder))

	for range 5 {
		h.clock.Advance(time.Minute)
		h.runOnce(t, scheduler.JobVerificationReminder)
	}

	rows := h.reminders.Rows()
	require.Len(t, rows, 1, "the reminder is raised once however often the job runs")
	assert.Equal(t, errand.ErrandVerificationReminder.Kind(), rows[0].Kind)
	assert.Equal(t, "e1:verify-reminder:5", rows[0].IdempotencyKey)
	assert.Equal(t, "e1", rows[0].ErrandID)
	assert.Equal(t, scheduler.ReminderData{
		ErrandID:      "e1",
		ErrandVersion: 5,
		Status:        errand.StatusDelivered,
		RequesterID:   requesterID,
		CourierID:     courierID,
		Deadline:      t0.Add(30 * time.Minute),
		At:            t0.Add(5 * time.Minute),
	}, rows[0].Payload)

	// Reminders change no errand state.
	e, _ := h.store.Errand("e1")
	assert.Equal(t, errand.StatusDelivered, e.Status)
	assert.Equal(t, 5, e.Version)
	assert.Empty(t, h.store.Transitions())
	assert.Empty(t, h.store.Outbox())
}

func TestVerificationReminderTimingComesFromSettings(t *testing.T) {
	h := newReminderHarness(t)
	settings := errand.DefaultSystemSettings()
	settings.Deadlines.VerificationAfter = time.Hour
	settings.Deadlines.VerificationReminderAfter = 10 * time.Minute
	h.settings.Set(settings)
	e := deliveredErrand("e1", t0.Add(time.Hour))
	e.DeliveredAt = ptr(t0)
	h.put(e)

	h.clock.Set(t0.Add(9 * time.Minute))
	h.runOnce(t, scheduler.JobVerificationReminder)
	assert.Empty(t, h.reminders.Rows())

	h.clock.Set(t0.Add(10 * time.Minute))
	h.runOnce(t, scheduler.JobVerificationReminder)
	assert.Equal(t, []string{"e1:verify-reminder:5"}, h.keys())
}

func TestVerificationReminderFailsWhenSettingsCannotBeRead(t *testing.T) {
	h := newReminderHarness(t)
	h.put(deliveredAt("e1", t0.Add(-10*time.Minute)))
	boom := errors.New("settings unavailable")
	h.settings.FailNext("Get", boom)

	_, err := h.sched.RunOnce(context.Background(), scheduler.JobVerificationReminder)

	require.ErrorIs(t, err, boom)
	assert.Empty(t, h.reminders.Rows())
	assert.Equal(t, uint64(1), h.sched.Stats()[scheduler.JobVerificationReminder].Failures)

	// The pickup reminder does not read settings, so it is unaffected.
	h.settings.FailNext("Get", boom)
	_, err = h.sched.RunOnce(context.Background(), scheduler.JobPickupReminder)
	require.NoError(t, err)
}

func TestPickupReminderIsRaisedAgainAfterReacceptance(t *testing.T) {
	ctx := context.Background()
	h := newReminderHarness(t)
	h.put(assignedErrand("e1", t0.Add(time.Hour), t0.Add(10*time.Minute)))
	h.put(assignedErrand("later", t0.Add(time.Hour), t0.Add(10*time.Minute+time.Second)))

	h.runOnce(t, scheduler.JobPickupReminder)
	h.runOnce(t, scheduler.JobPickupReminder)
	assert.Equal(t, []string{"e1:pickup-reminder:3"}, h.keys())
	assert.Equal(t, courierID, h.reminders.Rows()[0].Payload.(scheduler.ReminderData).CourierID)

	// The courier withdraws and another courier accepts: a new version.
	courier := errand.Actor{Type: errand.ActorCourier, ID: courierID}
	_, _, err := h.store.Fire(ctx, "e1", errand.EventWithdraw, courier, errand.EventInput{}, t0.Add(time.Minute))
	require.NoError(t, err)
	deadlines := errand.DefaultDeadlineSettings()
	deadlines.PickupAfter = 5 * time.Minute
	second := errand.Actor{Type: errand.ActorCourier, ID: "auth0|courier-2"}
	e, _, err := h.store.Fire(ctx, "e1", errand.EventAccept, second, errand.EventInput{Deadlines: deadlines}, t0.Add(2*time.Minute))
	require.NoError(t, err)
	require.Equal(t, 5, e.Version)

	h.clock.Set(t0.Add(2 * time.Minute))
	h.runOnce(t, scheduler.JobPickupReminder)
	h.runOnce(t, scheduler.JobPickupReminder)

	// The second run orders its candidates by deadline.
	assert.Equal(t, []string{"e1:pickup-reminder:3", "e1:pickup-reminder:5", "later:pickup-reminder:3"}, h.keys())
	last := h.reminders.Rows()[1].Payload.(scheduler.ReminderData)
	assert.Equal(t, "auth0|courier-2", last.CourierID)
	assert.Equal(t, t0.Add(7*time.Minute), last.Deadline)
}

func TestNoReminderForAnErrandThatLeftTheState(t *testing.T) {
	ctx := context.Background()
	requester := errand.Actor{Type: errand.ActorRequester, ID: requesterID}

	t.Run("before the run", func(t *testing.T) {
		h := newReminderHarness(t)
		h.put(deliveredAt("e1", t0.Add(-10*time.Minute)))
		_, _, err := h.store.Fire(ctx, "e1", errand.EventConfirm, requester, errand.EventInput{}, t0)
		require.NoError(t, err)

		assert.Equal(t, scheduler.BatchResult{}, h.runOnce(t, scheduler.JobVerificationReminder))
		assert.Empty(t, h.reminders.Rows())
	})

	t.Run("between selection and insert", func(t *testing.T) {
		h := newReminderHarness(t)
		h.put(deliveredAt("e1", t0.Add(-10*time.Minute)))
		h.reminders.beforeInsert = func(id string) {
			_, _, err := h.store.Fire(ctx, id, errand.EventConfirm, requester, errand.EventInput{}, t0)
			require.NoError(t, err)
		}

		res := h.runOnce(t, scheduler.JobVerificationReminder)

		assert.Equal(t, scheduler.BatchResult{Seen: 1, Applied: 0}, res)
		assert.Empty(t, h.reminders.Rows())
		assert.Equal(t, errand.StatusCompleted, h.status(t, "e1"))
		assert.Equal(t, uint64(1), h.sched.Stats()[scheduler.JobVerificationReminder].Skipped)
	})
}

func TestReminderLogsCarryNoPersonalData(t *testing.T) {
	h := newReminderHarness(t)
	h.put(deliveredAt("e1", t0.Add(-10*time.Minute)))
	h.put(assignedErrand("e2", t0.Add(time.Hour), t0.Add(5*time.Minute)))

	h.runOnce(t, scheduler.JobVerificationReminder)
	h.runOnce(t, scheduler.JobPickupReminder)

	logs := h.logs.String()
	assert.Contains(t, logs, `"msg":"scheduler raised reminder"`)
	assert.Contains(t, logs, `"job":"verification_reminder"`)
	assert.Contains(t, logs, `"job":"pickup_reminder"`)
	assert.Contains(t, logs, `"errand_id":"e1"`)
	for _, personal := range []string{requesterID, courierID, contactName, contactMobile} {
		assert.NotContains(t, logs, personal)
	}
}

func TestRunDrainsFullReminderBatches(t *testing.T) {
	store := testfakes.NewFakeStore()
	reminders := newFakeReminders(store)
	sched := scheduler.New(store, testfakes.NewFakeClock(t0), scheduler.Config{
		AcceptanceExpiryInterval:     time.Hour,
		PickupDeadlineInterval:       time.Hour,
		DeliveryDeadlineInterval:     time.Hour,
		VerificationLapseInterval:    time.Hour,
		VerificationReminderInterval: time.Hour,
		PickupReminderInterval:       time.Hour,
		BatchSize:                    2,
		Reminders:                    reminders,
		Settings:                     testfakes.NewFakeSettings(),
		Logger:                       slog.New(slog.DiscardHandler),
	})
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		store.Put(deliveredAt(id, t0.Add(-10*time.Minute)))
		reminders.track(id)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		sched.Run(ctx)
	}()
	defer func() {
		cancel()
		<-done
	}()

	require.Eventually(t, func() bool { return len(reminders.Rows()) == 5 }, 2*time.Second, 5*time.Millisecond)
	assert.Equal(t, uint64(3), sched.Stats()[scheduler.JobVerificationReminder].Batches)
}
