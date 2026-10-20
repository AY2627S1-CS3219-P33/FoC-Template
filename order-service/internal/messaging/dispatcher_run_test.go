package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/msgrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/resilience"
	"github.com/stretchr/testify/require"
)

// stubOutbox hands out its rows once per family and records every write.
type stubOutbox struct {
	mu          sync.Mutex
	rows        map[bool][]msgrepo.Row
	claims      int
	sent        []int64
	failed      []int64
	retried     []int64
	code        string
	delay       time.Duration
	markSentErr error
	resend      [][]msgrepo.Resent
	resendCalls int
	due         map[bool]int64
}

func newStubOutbox(rows ...msgrepo.Row) *stubOutbox {
	s := &stubOutbox{rows: map[bool][]msgrepo.Row{}, due: map[bool]int64{}}
	for _, row := range rows {
		credit := strings.HasPrefix(row.Kind, "credit.")
		s.rows[credit] = append(s.rows[credit], row)
	}
	return s
}

func (s *stubOutbox) Claim(_ context.Context, _ string, credit bool, limit int, _ time.Duration) ([]msgrepo.Row, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claims++
	n := min(limit, len(s.rows[credit]))
	rows := s.rows[credit][:n]
	s.rows[credit] = s.rows[credit][n:]
	return rows, nil
}

func (s *stubOutbox) MarkSent(_ context.Context, row msgrepo.Row) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.markSentErr != nil {
		return s.markSentErr
	}
	s.sent = append(s.sent, row.ID)
	return nil
}

func (s *stubOutbox) Reschedule(_ context.Context, row msgrepo.Row, delay time.Duration, code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retried = append(s.retried, row.ID)
	s.delay, s.code = delay, code
	return nil
}

func (s *stubOutbox) MarkFailed(_ context.Context, row msgrepo.Row, code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failed = append(s.failed, row.ID)
	s.code = code
	return nil
}

func (s *stubOutbox) ResendUnconfirmed(context.Context, time.Duration, int) ([]msgrepo.Resent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resendCalls++
	if len(s.resend) == 0 {
		return nil, nil
	}
	batch := s.resend[0]
	s.resend = s.resend[1:]
	return batch, nil
}

func (s *stubOutbox) CountDue(_ context.Context, credit bool) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.due[credit], nil
}

func (s *stubOutbox) sentIDs() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.sent)
}

// stubPublisher fails with err, calls onCall on every publish, and blocks
// publishes to an exchange in block until its channel is closed.
type stubPublisher struct {
	mu       sync.Mutex
	err      error
	calls    int
	block    map[string]chan struct{}
	onCall   func()
	messages []errand.Message
}

func (p *stubPublisher) Publish(ctx context.Context, m errand.Message) error {
	p.mu.Lock()
	p.calls++
	p.messages = append(p.messages, m)
	wait, onCall := p.block[m.Exchange], p.onCall
	p.mu.Unlock()
	if onCall != nil {
		onCall()
	}
	if wait != nil {
		select {
		case <-wait:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return p.err
}

type stubSettings struct{}

func (stubSettings) Get(context.Context) (errand.SystemSettings, error) {
	return errand.DefaultSystemSettings(), nil
}

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

func newTestDispatcher(t *testing.T, outbox Outbox, pub errand.Publisher, cfg DispatcherConfig) *Dispatcher {
	t.Helper()
	if cfg.WorkerID == "" {
		cfg.WorkerID = "test"
	}
	d, err := NewDispatcher(outbox, pub, stubSettings{}, discard, cfg)
	require.NoError(t, err)
	return d
}

func eventRow(id int64) msgrepo.Row {
	row := testRow(errand.ErrandOpened.Kind())
	row.ID = id
	row.Payload, _ = json.Marshal(errand.EventData{ErrandID: row.ErrandID, ErrandVersion: 2, At: row.CreatedAt})
	return row
}

func creditRows(n int) []msgrepo.Row {
	var rows []msgrepo.Row
	for i := range n {
		row := testRow(errand.KindCreditReserve)
		row.ID = int64(i + 1)
		rows = append(rows, row)
	}
	return rows
}

func TestDispatcherConfigDefaultsAndValidation(t *testing.T) {
	d := newTestDispatcher(t, newStubOutbox(), &stubPublisher{}, DispatcherConfig{})
	def := DefaultDispatcherConfig()
	require.Equal(t, def.PollInterval, d.cfg.PollInterval)
	require.Equal(t, 10, d.cfg.BatchSize)
	require.Equal(t, 10, d.cfg.MaxAttempts)
	require.Equal(t, 2*time.Minute, d.cfg.Lease)
	require.Equal(t, time.Minute, d.cfg.SweepInterval)
	require.Equal(t, resilience.DefaultBackoffBase, d.cfg.Backoff.Base)
	require.Equal(t, resilience.DefaultBackoffCap, d.cfg.Backoff.Cap)
	require.NotNil(t, d.Metrics())
	require.Equal(t, "test", d.WorkerID())

	for name, cfg := range map[string]DispatcherConfig{
		"lease shorter than a batch": {BatchSize: 20, PublishTimeout: 10 * time.Second, Lease: time.Minute},
		"batch too large":            {BatchSize: 501},
		"negative attempts":          {MaxAttempts: -1},
		"negative poll":              {PollInterval: -time.Second},
		"bad backoff":                {Backoff: resilience.BackoffPolicy{Base: time.Second, Cap: time.Millisecond}},
	} {
		_, err := NewDispatcher(newStubOutbox(), &stubPublisher{}, stubSettings{}, discard, cfg)
		require.Error(t, err, name)
	}
	_, err := NewDispatcher(nil, &stubPublisher{}, stubSettings{}, discard, DispatcherConfig{})
	require.Error(t, err)
}

func TestNewWorkerIDIsUniquePerProcess(t *testing.T) {
	host, err := os.Hostname()
	require.NoError(t, err)
	a, b := NewWorkerID(), NewWorkerID()
	require.NotEqual(t, a, b)
	require.True(t, strings.HasPrefix(a, host+"-"))
	d, err := NewDispatcher(newStubOutbox(), &stubPublisher{}, stubSettings{}, discard, DispatcherConfig{})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(d.WorkerID(), host+"-"), "an empty worker ID is generated")
}

func TestDispatcherDeliveryOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		event                 bool
		failure               error
		attempts              int32
		sent, retried, failed int
		code                  string
		exchange, key         string
	}{
		{name: "credit confirmed by broker", attempts: 1, sent: 1, exchange: errand.ExchangeCommands, key: errand.CommandCreditReserve},
		{name: "event confirmed by broker", event: true, attempts: 1, sent: 1, exchange: errand.ExchangeEvents, key: string(errand.ErrandOpened)},
		{name: "temporary failure", failure: errors.New("connection lost"), attempts: 1, retried: 1, code: CodePublishFailed},
		{name: "broker down", failure: ErrBrokerUnavailable, attempts: 1, retried: 1, code: CodeBrokerUnavailable},
		{name: "confirm timeout", failure: context.DeadlineExceeded, attempts: 1, retried: 1, code: CodePublishTimeout},
		{name: "unroutable", failure: ErrUnroutable, attempts: 1, retried: 1, code: CodeUnroutable},
		{name: "event failure", event: true, failure: errors.New("connection lost"), attempts: 1, retried: 1, code: CodePublishFailed},
		{name: "permanent", failure: errand.ErrUnpublishable, attempts: 1, failed: 1, code: CodeUnpublishable},
		{name: "last attempt", failure: errors.New("connection lost"), attempts: 10, failed: 1, code: CodePublishFailed},
		{name: "crashed last attempt", attempts: 11, failed: 1, code: CodeAttemptsExhausted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := testRow(errand.KindCreditReserve)
			if tc.event {
				row = eventRow(1)
			}
			row.Attempts = tc.attempts
			store := newStubOutbox(row)
			pub := &stubPublisher{err: tc.failure}
			counters := NewCounters()
			d := newTestDispatcher(t, store, pub, DispatcherConfig{Metrics: counters})
			full, err := d.dispatch(context.Background(), !tc.event)
			require.NoError(t, err)
			require.False(t, full)
			require.Len(t, store.sent, tc.sent)
			require.Len(t, store.retried, tc.retried)
			require.Len(t, store.failed, tc.failed)
			require.Equal(t, tc.code, store.code)
			require.EqualValues(t, tc.sent, counters.Count(CounterSent, row.Kind))
			require.EqualValues(t, tc.retried, counters.Count(CounterRescheduled, row.Kind))
			require.EqualValues(t, tc.failed, counters.Count(CounterFailed, row.Kind))
			if tc.retried > 0 {
				require.GreaterOrEqual(t, store.delay, time.Duration(0))
				require.LessOrEqual(t, store.delay, resilience.DefaultBackoffBase)
			}
			if tc.attempts > 10 {
				require.Zero(t, pub.calls)
			} else {
				require.Equal(t, 1, pub.calls, "published once")
			}
			if tc.exchange != "" {
				m := pub.messages[0]
				require.Equal(t, tc.exchange, m.Exchange)
				require.Equal(t, tc.key, m.RoutingKey)
				require.Equal(t, row.IdempotencyKey, m.Envelope.MessageID)
			}
		})
	}
}

func TestDispatcherBackoffGrowsWithAttempts(t *testing.T) {
	policy := resilience.BackoffPolicy{Base: time.Second, Cap: time.Minute, Rand: func(n int64) int64 { return n - 1 }}
	var delays []time.Duration
	for attempt := int32(1); attempt <= 4; attempt++ {
		row := testRow(errand.KindCreditRelease)
		row.Attempts = attempt
		store := newStubOutbox(row)
		d := newTestDispatcher(t, store, &stubPublisher{err: errors.New("down")}, DispatcherConfig{Backoff: policy})
		_, err := d.dispatch(context.Background(), true)
		require.NoError(t, err)
		delays = append(delays, store.delay)
	}
	require.Equal(t, []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}, delays)
}

func TestDispatcherReportsFullBatch(t *testing.T) {
	store := newStubOutbox(creditRows(3)...)
	d := newTestDispatcher(t, store, &stubPublisher{}, DispatcherConfig{BatchSize: 2})
	full, err := d.dispatch(context.Background(), true)
	require.NoError(t, err)
	require.True(t, full, "a full batch polls again at once")
	full, err = d.dispatch(context.Background(), true)
	require.NoError(t, err)
	require.False(t, full)
	require.Equal(t, []int64{1, 2, 3}, store.sentIDs())
}

func TestDispatcherIgnoresLostLease(t *testing.T) {
	store := newStubOutbox(testRow(errand.KindCreditReserve))
	store.markSentErr = msgrepo.ErrLeaseLost
	counters := NewCounters()
	d := newTestDispatcher(t, store, &stubPublisher{}, DispatcherConfig{Metrics: counters})
	_, err := d.dispatch(context.Background(), true)
	require.NoError(t, err, "a lost lease is logged, not an error")
	require.Zero(t, counters.Count(CounterSent, errand.KindCreditReserve))

	store = newStubOutbox(testRow(errand.KindCreditReserve))
	store.markSentErr = errors.New("database down")
	d = newTestDispatcher(t, store, &stubPublisher{}, DispatcherConfig{})
	_, err = d.dispatch(context.Background(), true)
	require.Error(t, err)
}

func TestDispatcherShutdownFinishesCurrentRowAndHandsBackTheRest(t *testing.T) {
	store := newStubOutbox(creditRows(3)...)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Shutdown arrives while the first row is being published.
	pub := &stubPublisher{onCall: cancel}
	d := newTestDispatcher(t, store, pub, DispatcherConfig{})
	_, err := d.dispatch(ctx, true)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, pub.calls)
	require.Equal(t, []int64{1}, store.sent, "the row in progress is finished")
	require.Equal(t, []int64{2, 3}, store.retried, "unpublished rows are handed back")
	require.Equal(t, CodeWorkerStopped, store.code)
	require.Zero(t, store.delay)

	claims := store.claims
	_, err = d.dispatch(ctx, true)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, claims, store.claims, "nothing new is claimed")
	require.ErrorIs(t, d.Run(ctx), context.Canceled)
}

func TestDispatcherLoopsAreIndependent(t *testing.T) {
	for _, blocked := range []string{errand.ExchangeCommands, errand.ExchangeEvents} {
		t.Run("blocked "+blocked, func(t *testing.T) {
			credit, event := creditRows(1)[0], eventRow(2)
			store := newStubOutbox(credit, event)
			release := make(chan struct{})
			pub := &stubPublisher{block: map[string]chan struct{}{blocked: release}}
			d := newTestDispatcher(t, store, pub, DispatcherConfig{PollInterval: 5 * time.Millisecond})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- d.Run(ctx) }()

			free, stuck := event.ID, credit.ID
			if blocked == errand.ExchangeEvents {
				free, stuck = credit.ID, event.ID
			}
			require.Eventually(t, func() bool { return slices.Contains(store.sentIDs(), free) }, 5*time.Second, 5*time.Millisecond, "the other loop keeps running")
			require.NotContains(t, store.sentIDs(), stuck)
			close(release)
			require.Eventually(t, func() bool { return slices.Contains(store.sentIDs(), stuck) }, 5*time.Second, 5*time.Millisecond)
			cancel()
			require.ErrorIs(t, <-done, context.Canceled)
		})
	}
}

func TestDispatcherSweepResendsUntilShortBatchAndRecordsBacklog(t *testing.T) {
	store := newStubOutbox()
	store.resend = [][]msgrepo.Resent{
		{{ID: 1, Kind: errand.KindCreditRelease}, {ID: 2, Kind: errand.KindCreditTransfer}},
		{{ID: 3, Kind: errand.KindCreditRelease}},
	}
	store.due = map[bool]int64{true: 4, false: 7}
	counters := NewCounters()
	d := newTestDispatcher(t, store, &stubPublisher{}, DispatcherConfig{SweepLimit: 2, Metrics: counters})
	require.NoError(t, d.sweep(context.Background()))
	require.Equal(t, 2, store.resendCalls, "a full batch is followed by another query")
	require.EqualValues(t, 2, counters.Count(CounterResent, errand.KindCreditRelease))
	require.EqualValues(t, 1, counters.Count(CounterResent, errand.KindCreditTransfer))
	require.EqualValues(t, 4, counters.LastBacklog(FamilyCredit))
	require.EqualValues(t, 7, counters.LastBacklog(FamilyEvent))
}

func TestJitterStaysWithinTwentyPercent(t *testing.T) {
	for range 1000 {
		d := jitter(time.Second)
		require.GreaterOrEqual(t, d, 800*time.Millisecond)
		require.LessOrEqual(t, d, 1200*time.Millisecond)
	}
	require.Zero(t, jitter(0))
}
