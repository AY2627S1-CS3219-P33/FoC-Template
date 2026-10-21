package messaging

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	mathrand "math/rand/v2"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/msgrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/resilience"
	"golang.org/x/sync/errgroup"
)

// Outbox is the delivery side of storage; business handlers insert messages
// through the errand Store in their own transaction.
type Outbox interface {
	Claim(context.Context, string, bool, int, time.Duration) ([]msgrepo.Row, error)
	MarkSent(context.Context, msgrepo.Row) error
	Reschedule(context.Context, msgrepo.Row, time.Duration, string) error
	MarkFailed(context.Context, msgrepo.Row, string) error
	ResendUnconfirmed(context.Context, time.Duration, int) ([]msgrepo.Resent, error)
	CountDue(context.Context, bool) (int64, error)
}

// Failure codes stored in outbox.last_failure_code.
const (
	CodePublishFailed     = "PUBLISH_FAILED"
	CodePublishTimeout    = "PUBLISH_TIMEOUT"
	CodeBrokerUnavailable = "BROKER_UNAVAILABLE"
	CodeUnroutable        = "UNROUTABLE"
	CodeUnpublishable     = "UNPUBLISHABLE"
	CodeAttemptsExhausted = "ATTEMPTS_EXHAUSTED"
	// CodeWorkerStopped marks a claimed row handed back unpublished at shutdown.
	CodeWorkerStopped = "WORKER_STOPPED"
)

// DispatcherConfig tunes the dispatcher. Zero fields take the defaults in
// DefaultDispatcherConfig. The confirm timeout for the unconfirmed sweep is
// the admin setting outboxConfirmTimeout, read on every sweep.
type DispatcherConfig struct {
	// WorkerID is the lease owner; empty means NewWorkerID().
	WorkerID string
	// PollInterval is the wait between claims when a loop's last batch was
	// not full; each wait is jittered by ±20%.
	PollInterval time.Duration
	BatchSize    int
	// Lease must outlast a whole batch of publish timeouts, so the last row
	// in a batch is still leased when its turn comes.
	Lease          time.Duration
	PublishTimeout time.Duration
	// MaxAttempts is how many publishes a row gets before it is FAILED.
	MaxAttempts int
	// SweepInterval is how often SENT credit rows are checked for a missing
	// result, and the backlog is measured. SweepLimit caps one query.
	SweepInterval time.Duration
	SweepLimit    int
	// Backoff spaces out publish retries of one row.
	Backoff resilience.BackoffPolicy
	// Metrics receives counters; nil means a fresh Counters.
	Metrics Metrics
}

// DefaultDispatcherConfig returns the defaults: poll 1 s, batch 10, lease
// 2 min, publish timeout 10 s, 10 attempts, sweep every minute (100 rows per
// query) and backoff base 2 s, cap 5 min.
func DefaultDispatcherConfig() DispatcherConfig {
	return DispatcherConfig{
		PollInterval:   time.Second,
		BatchSize:      10,
		Lease:          2 * time.Minute,
		PublishTimeout: 10 * time.Second,
		MaxAttempts:    10,
		SweepInterval:  time.Minute,
		SweepLimit:     100,
		Backoff:        resilience.DefaultBackoffPolicy(),
	}
}

func (c DispatcherConfig) withDefaults() (DispatcherConfig, error) {
	def := DefaultDispatcherConfig()
	if c.WorkerID == "" {
		c.WorkerID = NewWorkerID()
	}
	if c.PollInterval == 0 {
		c.PollInterval = def.PollInterval
	}
	if c.BatchSize == 0 {
		c.BatchSize = def.BatchSize
	}
	if c.Lease == 0 {
		c.Lease = def.Lease
	}
	if c.PublishTimeout == 0 {
		c.PublishTimeout = def.PublishTimeout
	}
	if c.MaxAttempts == 0 {
		c.MaxAttempts = def.MaxAttempts
	}
	if c.SweepInterval == 0 {
		c.SweepInterval = def.SweepInterval
	}
	if c.SweepLimit == 0 {
		c.SweepLimit = def.SweepLimit
	}
	if c.Backoff.Base == 0 && c.Backoff.Cap == 0 {
		c.Backoff.Base, c.Backoff.Cap = def.Backoff.Base, def.Backoff.Cap
	}
	if c.Metrics == nil {
		c.Metrics = NewCounters()
	}
	switch {
	case c.PollInterval < 0 || c.SweepInterval < 0 || c.PublishTimeout < 0:
		return c, errors.New("messaging: dispatcher intervals must be positive")
	case c.BatchSize < 1 || c.BatchSize > 500 || c.SweepLimit < 1 || c.SweepLimit > 500:
		return c, errors.New("messaging: batch size and sweep limit must be between 1 and 500")
	case c.MaxAttempts < 1:
		return c, errors.New("messaging: max attempts must be positive")
	case c.Lease <= time.Duration(c.BatchSize)*c.PublishTimeout:
		return c, errors.New("messaging: lease must exceed batch size times publish timeout")
	}
	if err := c.Backoff.Validate(); err != nil {
		return c, err
	}
	return c, nil
}

// NewWorkerID returns the host name plus a random suffix, so two processes
// on one host, or a restarted one, never share leases.
func NewWorkerID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "outbox"
	}
	return host + "-" + strings.ToLower(rand.Text()[:8])
}

type Dispatcher struct {
	outbox    Outbox
	publisher errand.Publisher
	settings  errand.Settings
	logger    *slog.Logger
	cfg       DispatcherConfig
}

func NewDispatcher(outbox Outbox, publisher errand.Publisher, settings errand.Settings, logger *slog.Logger, cfg DispatcherConfig) (*Dispatcher, error) {
	if outbox == nil || publisher == nil || settings == nil || logger == nil {
		return nil, errors.New("messaging: dispatcher dependencies are required")
	}
	cfg, err := cfg.withDefaults()
	if err != nil {
		return nil, err
	}
	return &Dispatcher{outbox: outbox, publisher: publisher, settings: settings, logger: logger.With(slog.String("worker", cfg.WorkerID)), cfg: cfg}, nil
}

// WorkerID is the lease owner this dispatcher writes.
func (d *Dispatcher) WorkerID() string { return d.cfg.WorkerID }

// Metrics returns the counters the dispatcher writes to.
func (d *Dispatcher) Metrics() Metrics { return d.cfg.Metrics }

// Run dispatches until ctx is cancelled, then returns ctx's error. Credit and
// event rows have separate loops so an event backlog never delays credit
// commands; a third loop runs the unconfirmed sweep.
//
// On cancellation a loop finishes the row it is publishing, hands the rest of
// its batch back unpublished, and claims nothing new. A crash instead leaves
// the leases to expire, and the rows are published again with the same
// messageId.
func (d *Dispatcher) Run(ctx context.Context) error {
	group, ctx := errgroup.WithContext(ctx)
	group.Go(func() error { return d.loop(ctx, true) })
	group.Go(func() error { return d.loop(ctx, false) })
	group.Go(func() error { return d.sweepLoop(ctx) })
	return group.Wait()
}

func (d *Dispatcher) loop(ctx context.Context, credit bool) error {
	for {
		full, err := d.dispatch(ctx, credit)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			d.logger.ErrorContext(ctx, "outbox dispatch failed", slog.String("family", family(credit)), slog.Any("error", err))
		}
		if full && err == nil {
			continue
		}
		if err := sleep(ctx, jitter(d.cfg.PollInterval)); err != nil {
			return err
		}
	}
}

// dispatch claims and handles one batch of one family. It reports whether
// the batch was full, so the loop polls again at once.
func (d *Dispatcher) dispatch(ctx context.Context, credit bool) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	rows, err := d.outbox.Claim(ctx, d.cfg.WorkerID, credit, d.cfg.BatchSize, d.cfg.Lease)
	if err != nil {
		return false, err
	}
	// A claimed row is finished even if ctx is cancelled meanwhile: shutdown
	// stops claiming, not handling. Each step has its own timeout.
	work := context.WithoutCancel(ctx)
	for i, row := range rows {
		if ctx.Err() != nil {
			d.handBack(work, rows[i:])
			return false, ctx.Err()
		}
		if err := d.handle(work, row); err != nil {
			return false, err
		}
	}
	return len(rows) == d.cfg.BatchSize, nil
}

func (d *Dispatcher) handle(ctx context.Context, row msgrepo.Row) error {
	if int(row.Attempts) > d.cfg.MaxAttempts {
		// A worker died during the last allowed attempt.
		return d.fail(ctx, row, CodeAttemptsExhausted)
	}
	message, err := MessageFromOutbox(row)
	if err == nil {
		publishCtx, cancel := context.WithTimeout(ctx, d.cfg.PublishTimeout)
		err = d.publisher.Publish(publishCtx, message)
		cancel()
	}
	if err == nil {
		return d.settle(ctx, row, d.outbox.MarkSent(ctx, row), func() {
			d.cfg.Metrics.Sent(row.Kind)
			d.logger.DebugContext(ctx, "outbox row sent", rowAttrs(row)...)
		})
	}
	code := failureCode(err)
	d.logger.WarnContext(ctx, "outbox publication failed", append(rowAttrs(row), slog.String("code", code))...)
	if errors.Is(err, errand.ErrUnpublishable) || int(row.Attempts) >= d.cfg.MaxAttempts {
		return d.fail(ctx, row, code)
	}
	delay := d.cfg.Backoff.Backoff(int(row.Attempts))
	return d.settle(ctx, row, d.outbox.Reschedule(ctx, row, delay, code), func() { d.cfg.Metrics.Rescheduled(row.Kind) })
}

func (d *Dispatcher) fail(ctx context.Context, row msgrepo.Row, code string) error {
	return d.settle(ctx, row, d.outbox.MarkFailed(ctx, row, code), func() {
		d.cfg.Metrics.Failed(row.Kind)
		d.logger.ErrorContext(ctx, "outbox row failed; needs an admin", append(rowAttrs(row), slog.String("code", code))...)
	})
}

// settle runs done after a lease-guarded write succeeds. A lost lease is
// logged and dropped: another worker owns the row now, or a credit result
// already settled it.
func (d *Dispatcher) settle(ctx context.Context, row msgrepo.Row, err error, done func()) error {
	if errors.Is(err, msgrepo.ErrLeaseLost) {
		d.logger.WarnContext(ctx, "outbox lease lost", rowAttrs(row)...)
		return nil
	}
	if err != nil {
		return err
	}
	done()
	return nil
}

// handBack releases rows claimed but not published at shutdown, so another
// worker can take them without waiting for the lease.
func (d *Dispatcher) handBack(ctx context.Context, rows []msgrepo.Row) {
	for _, row := range rows {
		if err := d.settle(ctx, row, d.outbox.Reschedule(ctx, row, 0, CodeWorkerStopped), func() {}); err != nil {
			d.logger.WarnContext(ctx, "outbox row not handed back; lease will expire", append(rowAttrs(row), slog.Any("error", err))...)
		}
	}
}

func (d *Dispatcher) sweepLoop(ctx context.Context) error {
	for {
		if err := d.sweep(ctx); err != nil && ctx.Err() == nil {
			d.logger.ErrorContext(ctx, "outbox sweep failed", slog.Any("error", err))
		}
		if err := sleep(ctx, jitter(d.cfg.SweepInterval)); err != nil {
			return err
		}
	}
}

// sweep re-sends SENT credit rows whose result is overdue (F4.6.2, F4.6.3)
// and records the backlog of each family.
func (d *Dispatcher) sweep(ctx context.Context) error {
	settings, err := d.settings.Get(ctx)
	if err != nil {
		return err
	}
	for {
		resent, err := d.outbox.ResendUnconfirmed(ctx, settings.OutboxConfirmTimeout, d.cfg.SweepLimit)
		if err != nil {
			return err
		}
		for _, r := range resent {
			d.cfg.Metrics.Resent(r.Kind)
			d.logger.WarnContext(ctx, "credit command unconfirmed; re-sending with the same key",
				slog.Int64("outbox_id", r.ID), slog.String("kind", r.Kind), slog.String("errand_id", r.ErrandID),
				slog.Int("attempts", int(r.Attempts)), slog.Time("sent_at", r.SentAt))
		}
		if len(resent) < d.cfg.SweepLimit {
			break
		}
	}
	for _, credit := range []bool{true, false} {
		n, err := d.outbox.CountDue(ctx, credit)
		if err != nil {
			return err
		}
		d.cfg.Metrics.Backlog(family(credit), n)
	}
	return nil
}

func failureCode(err error) string {
	switch {
	case errors.Is(err, errand.ErrUnpublishable):
		return CodeUnpublishable
	case errors.Is(err, ErrUnroutable):
		return CodeUnroutable
	case errors.Is(err, ErrBrokerUnavailable):
		return CodeBrokerUnavailable
	case errors.Is(err, context.DeadlineExceeded):
		return CodePublishTimeout
	}
	return CodePublishFailed
}

func rowAttrs(row msgrepo.Row) []any {
	return []any{slog.Int64("outbox_id", row.ID), slog.String("kind", row.Kind), slog.String("errand_id", row.ErrandID), slog.Int("attempt", int(row.Attempts))}
}

func family(credit bool) string {
	if credit {
		return FamilyCredit
	}
	return FamilyEvent
}

// jitter spreads d by ±20% so workers started together drift apart.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return time.Duration(float64(d) * (0.8 + 0.4*mathrand.Float64()))
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// MessageFromOutbox preserves the original identity, version and occurrence
// time on every retry; it never reads the errand's current version.
func MessageFromOutbox(row msgrepo.Row) (errand.Message, error) {
	var data struct {
		ErrandID      string    `json:"errandId"`
		ErrandVersion int       `json:"errandVersion"`
		At            time.Time `json:"at"`
	}
	if err := json.Unmarshal(row.Payload, &data); err != nil || data.ErrandID != row.ErrandID || data.ErrandVersion < 1 {
		return errand.Message{}, fmt.Errorf("%w: invalid outbox payload", errand.ErrUnpublishable)
	}
	m := errand.Message{Envelope: errand.Envelope{MessageID: row.IdempotencyKey, ErrandID: row.ErrandID, ErrandVersion: data.ErrandVersion, OccurredAt: row.CreatedAt, Data: row.Payload}}
	if slices.Contains(errand.CreditCommands(), row.Kind) {
		m.Exchange, m.RoutingKey = errand.ExchangeCommands, row.Kind
	} else if strings.HasPrefix(row.Kind, "event.") && slices.Contains(errand.EventNames(), errand.EventName(strings.TrimPrefix(row.Kind, "event."))) {
		m.Exchange, m.RoutingKey = errand.ExchangeEvents, strings.TrimPrefix(row.Kind, "event.")
		if data.At.IsZero() {
			return errand.Message{}, fmt.Errorf("%w: event occurrence time is required", errand.ErrUnpublishable)
		}
		m.Envelope.OccurredAt = data.At
	} else {
		return errand.Message{}, fmt.Errorf("%w: unknown outbox kind", errand.ErrUnpublishable)
	}
	m.Envelope.Type = m.RoutingKey
	if _, err := encode(m); err != nil {
		return errand.Message{}, err
	}
	return m, nil
}
