package scheduler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/scheduler"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/testfakes"
)

const (
	requesterID   = "auth0|requester-1"
	courierID     = "auth0|courier-1"
	contactName   = "Alex Tan"
	contactMobile = "+65 9123 4567"
	reward        = 10
)

var t0 = time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

// openErrand is an OPEN errand whose acceptance deadline is acceptBy.
func openErrand(id string, acceptBy time.Time) errand.Errand {
	return errand.Errand{
		ID:          id,
		Status:      errand.StatusOpen,
		Version:     2,
		RequesterID: requesterID,
		Details: errand.Details{
			SupplierID:               "supplier-1",
			SupplierVersionID:        "supplier-version-1",
			DeliveryBuildingID:       "building-1",
			DeliveryBuildingSnapshot: json.RawMessage(`{"name":"COM1"}`),
			DeliveryDetail:           "Level 2 lounge",
			Items:                    json.RawMessage(`[{"description":"iced latte"}]`),
			ContactName:              contactName,
			ContactMobile:            contactMobile,
			Reward:                   reward,
			AcceptanceDeadline:       acceptBy,
		},
		CreateIdempotencyKey: "create-" + id,
		CreatedAt:            t0.Add(-2 * time.Hour),
		UpdatedAt:            t0.Add(-2 * time.Hour),
	}
}

// assignedErrand is ASSIGNED to courierID with the given deadlines.
func assignedErrand(id string, acceptBy, pickupBy time.Time) errand.Errand {
	e := openErrand(id, acceptBy)
	e.Status = errand.StatusAssigned
	e.Version = 3
	e.CourierID = courierID
	e.PickupDeadline = ptr(pickupBy)
	e.DeliveryDeadline = ptr(pickupBy.Add(time.Hour))
	return e
}

// pickedUpErrand is PICKED_UP with the given delivery deadline.
func pickedUpErrand(id string, deliverBy time.Time) errand.Errand {
	e := assignedErrand(id, t0.Add(-time.Hour), deliverBy.Add(-time.Hour))
	e.Status = errand.StatusPickedUp
	e.Version = 4
	e.DeliveryDeadline = ptr(deliverBy)
	return e
}

// deliveredErrand is DELIVERED with the given verification deadline.
func deliveredErrand(id string, verifyBy time.Time) errand.Errand {
	e := pickedUpErrand(id, verifyBy)
	e.Status = errand.StatusDelivered
	e.Version = 5
	e.DeliveredAt = ptr(verifyBy.Add(-30 * time.Minute))
	e.VerificationDeadline = ptr(verifyBy)
	return e
}

// syncBuffer is a bytes.Buffer safe for the concurrent writes of Run.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type harness struct {
	store *testfakes.FakeStore
	clock *testfakes.FakeClock
	logs  *syncBuffer
	sched *scheduler.Scheduler
}

// newHarness returns a scheduler over a fresh FakeStore with the clock at
// t0. cfg's Logger is replaced by one writing JSON into h.logs.
func newHarness(t *testing.T, cfg scheduler.Config) *harness {
	t.Helper()
	return newHarnessOver(t, cfg, func(s *testfakes.FakeStore) errand.Store { return s })
}

// newHarnessOver is newHarness with the scheduler using wrap(store), for
// scripted ClaimDue behaviour.
func newHarnessOver(t *testing.T, cfg scheduler.Config, wrap func(*testfakes.FakeStore) errand.Store) *harness {
	t.Helper()
	h := &harness{store: testfakes.NewFakeStore(), clock: testfakes.NewFakeClock(t0), logs: &syncBuffer{}}
	cfg.Logger = slog.New(slog.NewJSONHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	h.sched = scheduler.New(wrap(h.store), h.clock, cfg)
	return h
}

func (h *harness) runOnce(t *testing.T, kind errand.DueKind) scheduler.BatchResult {
	t.Helper()
	res, err := h.sched.RunOnce(context.Background(), kind)
	require.NoError(t, err)
	return res
}

func (h *harness) status(t *testing.T, id string) errand.Status {
	t.Helper()
	e, ok := h.store.Errand(id)
	require.True(t, ok, "errand %s not stored", id)
	return e.Status
}

// outboxFor returns the kinds of the outbox rows written for errand id.
func (h *harness) outboxFor(id string) []string {
	var kinds []string
	for _, r := range h.store.Outbox() {
		if r.ErrandID == id {
			kinds = append(kinds, r.Kind)
		}
	}
	return kinds
}

// claimHook is an errand.Store whose ClaimDue can be intercepted.
type claimHook struct {
	*testfakes.FakeStore
	claimDue func(ctx context.Context, kind errand.DueKind, now time.Time, limit int, decide errand.DecideFunc) (int, error)
}

func (s *claimHook) ClaimDue(ctx context.Context, kind errand.DueKind, now time.Time, limit int, decide errand.DecideFunc) (int, error) {
	if s.claimDue != nil {
		return s.claimDue(ctx, kind, now, limit, decide)
	}
	return s.FakeStore.ClaimDue(ctx, kind, now, limit, decide)
}
