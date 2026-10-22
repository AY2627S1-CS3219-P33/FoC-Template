package creditmsg_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/creditmsg"
	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/database/outboxrepo"
)

// fakeStore returns fixed rows and records what the dispatcher did with them.
type fakeStore struct {
	rows       []outboxrepo.Row
	claimErr   error
	applied    bool
	updateErr  error
	sent       []int64
	rescheds   map[int64]time.Duration
	codes      map[int64]string
	failed     []int64
	lease      time.Duration
	claimLimit int
}

func newFakeStore(rows ...outboxrepo.Row) *fakeStore {
	return &fakeStore{rows: rows, applied: true, rescheds: map[int64]time.Duration{}, codes: map[int64]string{}}
}

func (f *fakeStore) Claim(_ context.Context, _ string, limit int, lease time.Duration) ([]outboxrepo.Row, error) {
	f.claimLimit, f.lease = limit, lease
	return f.rows, f.claimErr
}

func (f *fakeStore) MarkSent(_ context.Context, id int64, _ string) (bool, error) {
	f.sent = append(f.sent, id)
	return f.applied, f.updateErr
}

func (f *fakeStore) Reschedule(_ context.Context, id int64, _ string, delay time.Duration, code string) (bool, error) {
	f.rescheds[id], f.codes[id] = delay, code
	return f.applied, f.updateErr
}

func (f *fakeStore) MarkFailed(_ context.Context, id int64, _ string, code string) (bool, error) {
	f.failed = append(f.failed, id)
	f.codes[id] = code
	return f.applied, f.updateErr
}

type fakePublisher struct {
	errs      map[string]error
	published []string
}

func (f *fakePublisher) Publish(_ context.Context, routingKey string, _ []byte) error {
	f.published = append(f.published, routingKey)
	return f.errs[routingKey]
}

func row(id int64, key string, attempts int) outboxrepo.Row {
	return outboxrepo.Row{ID: id, Key: fmt.Sprintf("m%d", id), RoutingKey: key, Payload: []byte(`{}`), Status: "PENDING", Attempts: attempts}
}

func newDispatcher(t *testing.T, store creditmsg.OutboxStore, pub creditmsg.ResultPublisher, cfg creditmsg.DispatcherConfig) *creditmsg.Dispatcher {
	t.Helper()
	d, err := creditmsg.NewDispatcher(store, pub, slog.New(slog.NewTextHandler(io.Discard, nil)), cfg)
	require.NoError(t, err)
	return d
}

func TestDispatcherMarksPublishedRowsSent(t *testing.T) {
	store := newFakeStore(row(1, "credit.reserved", 1), row(2, "credit.released", 1))
	pub := &fakePublisher{}

	n, err := newDispatcher(t, store, pub, creditmsg.DispatcherConfig{}).RunOnce(context.Background())

	require.NoError(t, err)
	require.Equal(t, 2, n)
	require.Equal(t, []string{"credit.reserved", "credit.released"}, pub.published)
	require.Equal(t, []int64{1, 2}, store.sent)
	require.Equal(t, 20, store.claimLimit, "default batch size")
	require.Equal(t, 30*time.Second, store.lease, "default lease")
}

func TestDispatcherReschedulesAFailedPublishWithinTheBackoff(t *testing.T) {
	for name, c := range map[string]struct {
		err  error
		code string
	}{
		"broker down":    {fmt.Errorf("%w: connection failed", creditmsg.ErrBrokerUnavailable), "BROKER_UNAVAILABLE"},
		"unroutable":     {fmt.Errorf("%w (reply 312)", creditmsg.ErrUnroutable), "UNROUTABLE"},
		"timeout":        {context.DeadlineExceeded, "TIMEOUT"},
		"something else": {errors.New("amqp://user:secret@host failed"), "PUBLISH_FAILED"},
	} {
		store := newFakeStore(row(1, "credit.reserved", 3))
		pub := &fakePublisher{errs: map[string]error{"credit.reserved": c.err}}
		cfg := creditmsg.DispatcherConfig{RetryBackoff: creditmsg.Backoff{Base: time.Second, Cap: time.Minute}}

		_, err := newDispatcher(t, store, pub, cfg).RunOnce(context.Background())

		require.NoError(t, err, name)
		require.Empty(t, store.sent, name)
		require.Empty(t, store.failed, name)
		require.Contains(t, store.rescheds, int64(1), name)
		require.LessOrEqual(t, store.rescheds[1], 4*time.Second, name, "attempt 3 waits at most base*2^2")
		require.Equal(t, c.code, store.codes[1], name)
		require.NotContains(t, store.codes[1], "secret", name, "a failure code never holds error text")
	}
}

func TestDispatcherFailsARowThatCannotBePublished(t *testing.T) {
	store := newFakeStore(row(1, "credit.reserved", 1))
	pub := &fakePublisher{errs: map[string]error{"credit.reserved": fmt.Errorf("%w: bad", creditmsg.ErrUnpublishable)}}

	_, err := newDispatcher(t, store, pub, creditmsg.DispatcherConfig{}).RunOnce(context.Background())

	require.NoError(t, err)
	require.Equal(t, []int64{1}, store.failed, "a retry cannot help")
	require.Equal(t, "UNPUBLISHABLE", store.codes[1])
	require.Empty(t, store.rescheds)
}

func TestDispatcherFailsARowOnItsLastAttempt(t *testing.T) {
	store := newFakeStore(row(1, "credit.reserved", 4), row(2, "credit.released", 3))
	broker := fmt.Errorf("%w: down", creditmsg.ErrBrokerUnavailable)
	pub := &fakePublisher{errs: map[string]error{"credit.reserved": broker, "credit.released": broker}}

	_, err := newDispatcher(t, store, pub, creditmsg.DispatcherConfig{MaxAttempts: 4}).RunOnce(context.Background())

	require.NoError(t, err)
	require.Equal(t, []int64{1}, store.failed, "attempt 4 of 4")
	require.Contains(t, store.rescheds, int64(2), "attempt 3 of 4 is retried")
}

func TestDispatcherLeavesRowsAloneWhenShuttingDown(t *testing.T) {
	store := newFakeStore(row(1, "credit.reserved", 1))
	ctx, cancel := context.WithCancel(context.Background())
	pub := &cancellingPublisher{cancel: cancel}

	_, err := newDispatcher(t, store, pub, creditmsg.DispatcherConfig{}).RunOnce(ctx)

	require.NoError(t, err)
	require.Empty(t, store.sent)
	require.Empty(t, store.failed)
	require.Empty(t, store.rescheds, "the lease runs out and the row is claimed again")
}

type cancellingPublisher struct{ cancel context.CancelFunc }

func (p *cancellingPublisher) Publish(ctx context.Context, _ string, _ []byte) error {
	p.cancel()
	return ctx.Err()
}

func TestDispatcherToleratesALostLease(t *testing.T) {
	store := newFakeStore(row(1, "credit.reserved", 1))
	store.applied = false

	_, err := newDispatcher(t, store, &fakePublisher{}, creditmsg.DispatcherConfig{}).RunOnce(context.Background())

	require.NoError(t, err, "another worker owns the row now")
}

func TestDispatcherReportsDatabaseErrors(t *testing.T) {
	store := newFakeStore()
	store.claimErr = errors.New("db down")
	_, err := newDispatcher(t, store, &fakePublisher{}, creditmsg.DispatcherConfig{}).RunOnce(context.Background())
	require.ErrorContains(t, err, "db down")

	store = newFakeStore(row(1, "credit.reserved", 1))
	store.updateErr = errors.New("db down")
	_, err = newDispatcher(t, store, &fakePublisher{}, creditmsg.DispatcherConfig{}).RunOnce(context.Background())
	require.ErrorContains(t, err, "outbox row 1")
}

func TestDispatcherRunStopsOnCancel(t *testing.T) {
	store := newFakeStore()
	d := newDispatcher(t, store, &fakePublisher{}, creditmsg.DispatcherConfig{PollInterval: time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	time.Sleep(20 * time.Millisecond)
	cancel()

	require.ErrorIs(t, <-done, context.Canceled)
}

func TestNewDispatcherValidates(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, pub := newFakeStore(), &fakePublisher{}

	_, err := creditmsg.NewDispatcher(nil, pub, logger, creditmsg.DispatcherConfig{})
	require.Error(t, err)
	_, err = creditmsg.NewDispatcher(store, nil, logger, creditmsg.DispatcherConfig{})
	require.Error(t, err)
	_, err = creditmsg.NewDispatcher(store, pub, logger, creditmsg.DispatcherConfig{BatchSize: 5000})
	require.Error(t, err)
	_, err = creditmsg.NewDispatcher(store, pub, logger, creditmsg.DispatcherConfig{MaxAttempts: -1})
	require.Error(t, err)
	d, err := creditmsg.NewDispatcher(store, pub, logger, creditmsg.DispatcherConfig{})
	require.NoError(t, err)
	require.NotEmpty(t, d.WorkerID())
}
