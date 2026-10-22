package creditmsg

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/database/outboxrepo"
)

// OutboxStore is the part of the outbox the dispatcher uses.
// outboxrepo.Store implements it.
type OutboxStore interface {
	Claim(ctx context.Context, worker string, limit int, lease time.Duration) ([]outboxrepo.Row, error)
	MarkSent(ctx context.Context, id int64, worker string) (bool, error)
	Reschedule(ctx context.Context, id int64, worker string, delay time.Duration, code string) (bool, error)
	MarkFailed(ctx context.Context, id int64, worker, code string) (bool, error)
}

// ResultPublisher sends a result message and returns when the broker has
// confirmed it. *Publisher implements it.
type ResultPublisher interface {
	Publish(ctx context.Context, routingKey string, payload []byte) error
}

// Codes stored in outbox.last_failure_code.
const (
	failureUnpublishable = "UNPUBLISHABLE"
	failureUnroutable    = "UNROUTABLE"
	failureBroker        = "BROKER_UNAVAILABLE"
	failureTimeout       = "TIMEOUT"
	failureOther         = "PUBLISH_FAILED"
)

// DispatcherConfig tunes the dispatcher. Zero fields take the defaults from
// DefaultDispatcherConfig.
type DispatcherConfig struct {
	// PollInterval is how long to wait when no row is due.
	PollInterval time.Duration
	// BatchSize is the most rows claimed at once.
	BatchSize int
	// Lease is how long a claimed row belongs to the worker. If the worker
	// dies, the row can be claimed again after this time.
	Lease time.Duration
	// MaxAttempts is how often a row is published before it becomes FAILED.
	MaxAttempts int
	// RetryBackoff spaces out the attempts for one row.
	RetryBackoff Backoff
}

// DefaultDispatcherConfig returns a 1 s poll, batches of 20, a 30 s lease, 10
// attempts and retry backoff from 2 s to 5 min, as in order-service's outbox.
func DefaultDispatcherConfig() DispatcherConfig {
	return DispatcherConfig{
		PollInterval: time.Second,
		BatchSize:    20,
		Lease:        30 * time.Second,
		MaxAttempts:  10,
		RetryBackoff: Backoff{Base: 2 * time.Second, Cap: 5 * time.Minute},
	}
}

func (c DispatcherConfig) withDefaults() (DispatcherConfig, error) {
	def := DefaultDispatcherConfig()
	if c.PollInterval == 0 {
		c.PollInterval = def.PollInterval
	}
	if c.BatchSize == 0 {
		c.BatchSize = def.BatchSize
	}
	if c.Lease == 0 {
		c.Lease = def.Lease
	}
	if c.MaxAttempts == 0 {
		c.MaxAttempts = def.MaxAttempts
	}
	if c.RetryBackoff == (Backoff{}) {
		c.RetryBackoff = def.RetryBackoff
	}
	switch {
	case c.PollInterval < 0, c.Lease < 0:
		return c, errors.New("creditmsg: poll interval and lease must be positive")
	case c.BatchSize < 1 || c.BatchSize > 1000:
		return c, errors.New("creditmsg: batch size must be between 1 and 1000")
	case c.MaxAttempts < 1:
		return c, errors.New("creditmsg: max attempts must be at least 1")
	case c.RetryBackoff.Base <= 0 || c.RetryBackoff.Cap < c.RetryBackoff.Base:
		return c, errors.New("creditmsg: backoff base must be positive and at most the cap")
	}
	return c, nil
}

// Dispatcher publishes the rows of the result outbox. Rows are claimed with a
// lease, so several dispatchers can run on one table without publishing the
// same row at the same time. Delivery is at least once: a worker that dies
// after the broker confirmed a message and before MarkSent leaves a row that
// is published again with the same messageId, and order-service drops
// duplicates.
type Dispatcher struct {
	store     OutboxStore
	publisher ResultPublisher
	logger    *slog.Logger
	cfg       DispatcherConfig
	worker    string
}

func NewDispatcher(store OutboxStore, publisher ResultPublisher, logger *slog.Logger, cfg DispatcherConfig) (*Dispatcher, error) {
	if store == nil || publisher == nil || logger == nil {
		return nil, errors.New("creditmsg: dispatcher dependencies are required")
	}
	cfg, err := cfg.withDefaults()
	if err != nil {
		return nil, err
	}
	return &Dispatcher{store: store, publisher: publisher, logger: logger, cfg: cfg, worker: newWorkerID()}, nil
}

// WorkerID names this dispatcher in the lease columns.
func (d *Dispatcher) WorkerID() string { return d.worker }

func newWorkerID() string {
	host, _ := os.Hostname()
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	return fmt.Sprintf("%s-%d-%s", host, os.Getpid(), hex.EncodeToString(suffix))
}

// Run publishes rows until ctx is cancelled, then returns ctx's error. A
// database error is logged and retried after the poll interval.
func (d *Dispatcher) Run(ctx context.Context) error {
	for {
		n, err := d.RunOnce(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			d.logger.ErrorContext(ctx, "result outbox dispatch failed", slog.Any("error", err))
		}
		if n == 0 || err != nil {
			if err := sleep(ctx, d.cfg.PollInterval); err != nil {
				return err
			}
		}
	}
}

// RunOnce claims one batch and publishes it. It returns how many rows it
// claimed.
func (d *Dispatcher) RunOnce(ctx context.Context) (int, error) {
	rows, err := d.store.Claim(ctx, d.worker, d.cfg.BatchSize, d.cfg.Lease)
	if err != nil {
		return 0, err
	}
	for _, row := range rows {
		if err := d.dispatch(ctx, row); err != nil {
			return len(rows), err
		}
	}
	return len(rows), nil
}

// dispatch publishes one claimed row and records the outcome. It returns an
// error only when the outcome cannot be recorded.
func (d *Dispatcher) dispatch(ctx context.Context, row outboxrepo.Row) error {
	err := d.publisher.Publish(ctx, row.RoutingKey, row.Payload)
	if err == nil {
		return d.record(ctx, row, "sent")(d.store.MarkSent(ctx, row.ID, d.worker))
	}
	if ctx.Err() != nil {
		// Shutting down: leave the row; its lease runs out and it is claimed again.
		return nil
	}
	code := failureCode(err)
	switch {
	case errors.Is(err, ErrUnpublishable):
		d.logger.ErrorContext(ctx, "result cannot be published; marking it failed",
			slog.String("message_id", row.Key), slog.String("routing_key", row.RoutingKey), slog.Any("error", err))
		return d.record(ctx, row, "failed")(d.store.MarkFailed(ctx, row.ID, d.worker, code))
	case row.Attempts >= d.cfg.MaxAttempts:
		// A repeat of the command puts the row back to PENDING (outboxrepo.Enqueue),
		// and order-service sends the command again if it gets no result.
		d.logger.ErrorContext(ctx, "result not published after the last attempt; marking it failed",
			slog.String("message_id", row.Key), slog.Int("attempts", row.Attempts), slog.String("failure", code))
		return d.record(ctx, row, "failed")(d.store.MarkFailed(ctx, row.ID, d.worker, code))
	}
	delay := d.cfg.RetryBackoff.delay(row.Attempts)
	d.logger.WarnContext(ctx, "result not published; will retry",
		slog.String("message_id", row.Key), slog.Int("attempts", row.Attempts),
		slog.String("failure", code), slog.Duration("retry_in", delay))
	return d.record(ctx, row, "rescheduled")(d.store.Reschedule(ctx, row.ID, d.worker, delay, code))
}

// record wraps the outcome of a store update. A row the worker no longer
// holds the lease on is not an error: another worker owns it now.
func (d *Dispatcher) record(ctx context.Context, row outboxrepo.Row, outcome string) func(bool, error) error {
	return func(applied bool, err error) error {
		if err != nil {
			return fmt.Errorf("recording %s for outbox row %d: %w", outcome, row.ID, err)
		}
		if !applied {
			d.logger.WarnContext(ctx, "outbox row lease was lost; outcome not recorded",
				slog.String("message_id", row.Key), slog.String("outcome", outcome))
		}
		return nil
	}
}

// failureCode names a publish error in a few words for outbox.last_failure_code.
func failureCode(err error) string {
	switch {
	case errors.Is(err, ErrUnpublishable):
		return failureUnpublishable
	case errors.Is(err, ErrUnroutable):
		return failureUnroutable
	case errors.Is(err, ErrBrokerUnavailable):
		return failureBroker
	case errors.Is(err, context.DeadlineExceeded):
		return failureTimeout
	default:
		return failureOther
	}
}
