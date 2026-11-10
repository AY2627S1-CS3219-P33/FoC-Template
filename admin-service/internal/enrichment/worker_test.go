package enrichment

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/facts"
	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/orderclient"
)

var t0 = time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)

type fakeStore struct {
	mu       sync.Mutex
	due      []string // queue of errands that owe enrichment
	stored   []facts.Enrichment
	tried    []string
	retryAt  []time.Time
	storeErr error
}

func (f *fakeStore) EnrichmentDue(_ context.Context, retryBefore time.Time, limit int) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.retryAt = append(f.retryAt, retryBefore)
	n := min(limit, len(f.due))
	ids := append([]string(nil), f.due[:n]...)
	f.due = f.due[n:]
	return ids, nil
}

func (f *fakeStore) StoreEnrichment(_ context.Context, e facts.Enrichment, _ time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.storeErr != nil {
		return false, f.storeErr
	}
	f.stored = append(f.stored, e)
	return true, nil
}

func (f *fakeStore) MarkEnrichmentTried(_ context.Context, id string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tried = append(f.tried, id)
	return nil
}

type fakeSource struct {
	mu      sync.Mutex
	errands map[string]orderclient.Errand
	err     error
	calls   [][]string
}

func (f *fakeSource) ErrandsByID(_ context.Context, ids []string) ([]orderclient.Errand, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string(nil), ids...))
	if f.err != nil {
		return nil, f.err
	}
	var out []orderclient.Errand
	for _, id := range ids {
		if e, ok := f.errands[id]; ok {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeSource) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func str(s string) *string { return &s }

func errand(id string, supplierName *string) orderclient.Errand {
	return orderclient.Errand{
		ErrandID: id, SupplierID: "supplier-" + id, SupplierName: supplierName,
		DeliveryBuildingID: "building-" + id, DeliveryBuildingName: str("COM1"),
	}
}

func newWorker(t *testing.T, store *fakeStore, source *fakeSource, cfg Config) *Worker {
	t.Helper()
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return t0 }
	}
	worker, err := NewWorker(store, source, slog.New(slog.NewTextHandler(io.Discard, nil)), cfg)
	require.NoError(t, err)
	return worker
}

func TestPassStoresWhatOrderServiceExported(t *testing.T) {
	store := &fakeStore{due: []string{"a", "b"}}
	source := &fakeSource{errands: map[string]orderclient.Errand{
		"a": errand("a", str("Kopi Corner")),
		"b": errand("b", nil), // supplier-service was down for order-service
	}}

	tried, err := newWorker(t, store, source, Config{RetryAfter: time.Minute}).Pass(context.Background())

	require.NoError(t, err)
	require.Equal(t, 2, tried)
	require.Equal(t, []facts.Enrichment{
		{ErrandID: "a", SupplierID: "supplier-a", SupplierName: str("Kopi Corner"), DeliveryBuildingID: "building-a", DeliveryBuildingName: str("COM1")},
		{ErrandID: "b", SupplierID: "supplier-b", SupplierName: nil, DeliveryBuildingID: "building-b", DeliveryBuildingName: str("COM1")},
	}, store.stored, "a null supplier name is stored as null; the store keeps the errand due")
	require.Equal(t, []time.Time{t0.Add(-time.Minute)}, store.retryAt, "errands tried within RetryAfter are not asked about")
	require.Equal(t, [][]string{{"a", "b"}}, source.calls, "one request for the batch")
}

func TestPassMarksAnErrandOrderServiceDoesNotKnowAsTried(t *testing.T) {
	store := &fakeStore{due: []string{"a", "gone"}}
	source := &fakeSource{errands: map[string]orderclient.Errand{"a": errand("a", str("Kopi Corner"))}}

	_, err := newWorker(t, store, source, Config{}).Pass(context.Background())

	require.NoError(t, err)
	require.Len(t, store.stored, 1)
	require.Equal(t, []string{"gone"}, store.tried, "tried, so it waits the retry time; what is stored is untouched")
}

func TestPassWithNothingDueDoesNotCallOrderService(t *testing.T) {
	store, source := &fakeStore{}, &fakeSource{}

	tried, err := newWorker(t, store, source, Config{}).Pass(context.Background())

	require.NoError(t, err)
	require.Zero(t, tried)
	require.Zero(t, source.callCount())
}

func TestPassFailsWhenOrderServiceIsDown(t *testing.T) {
	store := &fakeStore{due: []string{"a"}}
	source := &fakeSource{err: orderclient.ErrUnavailable}

	_, err := newWorker(t, store, source, Config{}).Pass(context.Background())

	require.ErrorIs(t, err, orderclient.ErrUnavailable)
	require.Empty(t, store.stored)
	require.Empty(t, store.tried, "nothing is marked tried, so the errand is asked about again at once")
}

func TestPassStopsOnAStoreFailure(t *testing.T) {
	store := &fakeStore{due: []string{"a"}, storeErr: errors.New("db down")}
	source := &fakeSource{errands: map[string]orderclient.Errand{"a": errand("a", str("x"))}}

	_, err := newWorker(t, store, source, Config{}).Pass(context.Background())

	require.Error(t, err)
}

func TestRunDrainsEveryBatchAtStart(t *testing.T) {
	ids := make([]string, 0, 250)
	errands := map[string]orderclient.Errand{}
	for i := range 250 {
		id := string(rune('A'+i%26)) + string(rune('a'+i/26))
		ids = append(ids, id)
		errands[id] = errand(id, str("name"))
	}
	store, source := &fakeStore{due: ids}, &fakeSource{errands: errands}
	worker := newWorker(t, store, source, Config{Interval: time.Hour, Batch: 100})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()

	require.Eventually(t, func() bool { return source.callCount() == 3 }, 2*time.Second, 5*time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)

	require.Equal(t, []int{100, 100, 50}, []int{len(source.calls[0]), len(source.calls[1]), len(source.calls[2])})
	require.Len(t, store.stored, 250)
}

func TestWakeTriggersAPassWithoutWaitingForTheInterval(t *testing.T) {
	store := &fakeStore{}
	source := &fakeSource{errands: map[string]orderclient.Errand{"a": errand("a", str("Kopi Corner"))}}
	worker := newWorker(t, store, source, Config{Interval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	time.Sleep(50 * time.Millisecond) // the first pass finds nothing

	store.mu.Lock()
	store.due = []string{"a"}
	store.mu.Unlock()
	worker.Wake()
	worker.Wake() // wakes coalesce
	worker.Wake()

	require.Eventually(t, func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return len(store.stored) == 1
	}, 2*time.Second, 5*time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
}

func TestAFailedPassPausesForTheIntervalWhateverWakesIt(t *testing.T) {
	store := &fakeStore{due: []string{"a"}}
	source := &fakeSource{err: orderclient.ErrUnavailable}
	worker := newWorker(t, store, source, Config{Interval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	require.Eventually(t, func() bool { return source.callCount() == 1 }, 2*time.Second, 5*time.Millisecond)

	for range 5 {
		worker.Wake()
	}
	time.Sleep(100 * time.Millisecond)

	require.Equal(t, 1, source.callCount(), "events arriving during an outage do not hammer order-service")
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
}

func TestNewWorkerValidates(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, err := NewWorker(nil, &fakeSource{}, logger, Config{})
	require.Error(t, err)
	_, err = NewWorker(&fakeStore{}, nil, logger, Config{})
	require.Error(t, err)
	_, err = NewWorker(&fakeStore{}, &fakeSource{}, nil, Config{})
	require.Error(t, err)
	_, err = NewWorker(&fakeStore{}, &fakeSource{}, logger, Config{Batch: 101})
	require.Error(t, err)
	_, err = NewWorker(&fakeStore{}, &fakeSource{}, logger, Config{Interval: -1})
	require.Error(t, err)
}
