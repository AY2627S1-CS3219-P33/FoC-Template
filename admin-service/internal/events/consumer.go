// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-10-20
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

package events

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/resilience"
)

// Broker topology. foc.events is shared with every publisher, which declare
// it idempotently with these arguments; this queue is admin-service's own.
const (
	ExchangeEvents = "foc.events"
	Queue          = "admin-service.events"
	QueueDLQ       = "admin-service.events.dlq"
	// Binding receives every errand event. Binding a new queue does not
	// change what other consumers receive.
	Binding = "errand.#"
	// DeliveryLimit dead-letters a message that keeps failing, so one bad
	// message cannot block the queue forever.
	DeliveryLimit = 5
)

var errConnectionLost = errors.New("events: broker connection lost")

// ErrBrokerUnavailable means the broker could not be reached or set up.
var ErrBrokerUnavailable = errors.New("events: broker unavailable")

// Config tunes the consumer. Zero fields take the defaults.
type Config struct {
	// Prefetch bounds the unacknowledged deliveries the broker sends ahead.
	Prefetch int
	// ReconnectBackoff spaces out reconnects after a lost or failed session.
	ReconnectBackoff resilience.BackoffPolicy
	// RetryBackoff is the wait before requeueing a delivery after a
	// transient failure, growing with consecutive failures.
	RetryBackoff resilience.BackoffPolicy
	// HandleTimeout bounds the database work for one delivery.
	HandleTimeout time.Duration
}

func (c Config) withDefaults() (Config, error) {
	if c.Prefetch == 0 {
		c.Prefetch = 16
	}
	if c.ReconnectBackoff.Base == 0 && c.ReconnectBackoff.Cap == 0 {
		c.ReconnectBackoff = resilience.BackoffPolicy{Base: 500 * time.Millisecond, Cap: 30 * time.Second}
	}
	if c.RetryBackoff.Base == 0 && c.RetryBackoff.Cap == 0 {
		c.RetryBackoff = resilience.BackoffPolicy{Base: time.Second, Cap: 30 * time.Second}
	}
	if c.HandleTimeout == 0 {
		c.HandleTimeout = 30 * time.Second
	}
	switch {
	case c.Prefetch < 1 || c.Prefetch > 1000:
		return c, errors.New("events: prefetch must be between 1 and 1000")
	case c.HandleTimeout < 0:
		return c, errors.New("events: handle timeout must be positive")
	}
	if err := c.ReconnectBackoff.Validate(); err != nil {
		return c, err
	}
	return c, c.RetryBackoff.Validate()
}

// DeclareTopology declares the exchange, the queue with its dead-letter
// queue, and the binding. It runs on every (re)connect.
func DeclareTopology(ch *amqp.Channel) error {
	if err := ch.ExchangeDeclare(ExchangeEvents, "topic", true, false, false, false, nil); err != nil {
		return err
	}
	if _, err := ch.QueueDeclare(QueueDLQ, true, false, false, false, amqp.Table{"x-queue-type": "quorum"}); err != nil {
		return err
	}
	if _, err := ch.QueueDeclare(Queue, true, false, false, false, amqp.Table{
		"x-queue-type": "quorum", "x-delivery-limit": int32(DeliveryLimit),
		"x-dead-letter-exchange": "", "x-dead-letter-routing-key": QueueDLQ,
	}); err != nil {
		return err
	}
	return ch.QueueBind(Queue, Binding, ExchangeEvents, false, nil)
}

// Consumer reads admin-service.events and settles each delivery as its
// Handler says: ack after the work commits, requeue on a transient failure,
// reject (dead-letter) a message that can never be processed.
//
// Deliveries are handled one at a time, in order.
type Consumer struct {
	url     string
	handler *Handler
	logger  *slog.Logger
	cfg     Config
	running atomic.Bool
}

// NewConsumer validates configuration; Run opens the connection.
func NewConsumer(url string, handler *Handler, logger *slog.Logger, cfg Config) (*Consumer, error) {
	if url == "" {
		return nil, errors.New("events: AMQP URL is required")
	}
	if _, err := amqp.ParseURI(url); err != nil {
		return nil, errors.New("events: invalid AMQP URL")
	}
	if handler == nil || logger == nil {
		return nil, errors.New("events: consumer dependencies are required")
	}
	cfg, err := cfg.withDefaults()
	if err != nil {
		return nil, err
	}
	return &Consumer{url: url, handler: handler, logger: logger, cfg: cfg}, nil
}

// Running reports whether the consumer is subscribed to the queue, for
// readiness checks.
func (c *Consumer) Running() bool { return c.running.Load() }

// Run consumes until ctx is cancelled, then returns ctx's error. A lost or
// failed session is retried with backoff. On cancellation the delivery being
// handled is finished and settled; prefetched deliveries go back to the queue
// when the connection closes.
func (c *Consumer) Run(ctx context.Context) error {
	failures := 0
	for {
		subscribed, err := c.session(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if subscribed {
			failures = 0
		}
		failures++
		delay := c.cfg.ReconnectBackoff.Backoff(failures)
		c.logger.WarnContext(ctx, "event consumer disconnected; reconnecting",
			slog.Any("error", err), slog.Duration("retry_in", delay), slog.Int("failures", failures))
		if err := sleep(ctx, delay); err != nil {
			return err
		}
	}
}

// session runs one connection until it is lost or ctx is cancelled. It
// reports whether it got as far as subscribing.
func (c *Consumer) session(ctx context.Context) (bool, error) {
	conn, err := amqp.DialConfig(c.url, amqp.Config{
		Dial:       amqp.DefaultDial(10 * time.Second),
		Properties: amqp.Table{"connection_name": "admin-service events"},
	})
	if err != nil {
		// Dial errors can contain the URI, including credentials.
		return false, fmt.Errorf("%w: connection failed", ErrBrokerUnavailable)
	}
	defer func() { _ = conn.CloseDeadline(time.Now().Add(time.Second)) }()

	ch, err := conn.Channel()
	if err == nil {
		err = DeclareTopology(ch)
	}
	if err == nil {
		err = ch.Qos(c.cfg.Prefetch, 0, false)
	}
	var deliveries <-chan amqp.Delivery
	if err == nil {
		deliveries, err = ch.Consume(Queue, "", false, false, false, false, nil)
	}
	if err != nil {
		return false, fmt.Errorf("%w: setup failed: %w", ErrBrokerUnavailable, err)
	}

	c.running.Store(true)
	defer c.running.Store(false)
	c.logger.InfoContext(ctx, "consuming errand events", slog.String("queue", Queue), slog.Int("prefetch", c.cfg.Prefetch))

	retries := 0
	for {
		select {
		case <-ctx.Done():
			return true, ctx.Err()
		case d, ok := <-deliveries:
			if !ok {
				return true, errConnectionLost
			}
			c.settle(ctx, d, &retries)
		}
	}
}

// settle handles one delivery and acks, requeues or rejects it. retries
// counts consecutive transient failures, for the retry backoff.
func (c *Consumer) settle(ctx context.Context, d amqp.Delivery, retries *int) {
	disposition := c.handle(ctx, d)
	var err error
	switch disposition {
	case Ack:
		*retries = 0
		err = d.Ack(false)
	case Reject:
		*retries = 0
		err = d.Nack(false, false)
	default:
		*retries++
		// A cancelled wait still requeues at once, for another replica.
		_ = sleep(ctx, c.cfg.RetryBackoff.Backoff(*retries))
		err = d.Nack(false, true)
	}
	if err != nil {
		c.logger.WarnContext(ctx, "event not settled; the broker will redeliver it",
			slog.String("routing_key", d.RoutingKey), slog.String("message_id", d.MessageId),
			slog.String("disposition", disposition.String()), slog.Any("error", err))
	}
}

// handle runs the handler with its own timeout. The work is not cancelled by
// shutdown, so a delivery that started is finished and settled. A panic
// requeues the delivery; the delivery limit dead-letters one that always
// panics.
func (c *Consumer) handle(ctx context.Context, d amqp.Delivery) (disposition Disposition) {
	defer func() {
		if p := recover(); p != nil {
			c.logger.ErrorContext(ctx, "event handler panicked",
				slog.String("routing_key", d.RoutingKey), slog.String("message_id", d.MessageId), slog.Any("panic", p))
			disposition = Requeue
		}
	}()
	work, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.cfg.HandleTimeout)
	defer cancel()
	return c.handler.Handle(work, d.RoutingKey, d.Body)
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
