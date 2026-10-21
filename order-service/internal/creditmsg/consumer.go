package creditmsg

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/messaging"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/resilience"
	amqp "github.com/rabbitmq/amqp091-go"
)

var errConnectionLost = errors.New("creditmsg: broker connection lost")

// Config tunes the consumer. Zero fields take the defaults in
// DefaultConfig.
type Config struct {
	// Prefetch bounds the unacknowledged deliveries the broker sends ahead.
	Prefetch int
	// ReconnectBackoff spaces out reconnects after a lost or failed session.
	ReconnectBackoff resilience.BackoffPolicy
	// RetryBackoff is the wait before requeueing a delivery after a
	// transient failure, growing with consecutive failures. The queue's
	// delivery limit (5) dead-letters a result that keeps failing; the
	// dispatcher's unconfirmed sweep then re-sends its command and
	// credit-service answers again.
	RetryBackoff resilience.BackoffPolicy
	// HandleTimeout bounds the database work for one delivery.
	HandleTimeout time.Duration
}

// DefaultConfig returns prefetch 16, reconnect backoff 500 ms to 30 s, retry
// backoff 1 s to 30 s and a 30 s handle timeout.
func DefaultConfig() Config {
	return Config{
		Prefetch:         16,
		ReconnectBackoff: resilience.BackoffPolicy{Base: 500 * time.Millisecond, Cap: 30 * time.Second},
		RetryBackoff:     resilience.BackoffPolicy{Base: time.Second, Cap: 30 * time.Second},
		HandleTimeout:    30 * time.Second,
	}
}

func (c Config) withDefaults() (Config, error) {
	def := DefaultConfig()
	if c.Prefetch == 0 {
		c.Prefetch = def.Prefetch
	}
	if c.ReconnectBackoff.Base == 0 && c.ReconnectBackoff.Cap == 0 {
		c.ReconnectBackoff.Base, c.ReconnectBackoff.Cap = def.ReconnectBackoff.Base, def.ReconnectBackoff.Cap
	}
	if c.RetryBackoff.Base == 0 && c.RetryBackoff.Cap == 0 {
		c.RetryBackoff.Base, c.RetryBackoff.Cap = def.RetryBackoff.Base, def.RetryBackoff.Cap
	}
	if c.HandleTimeout == 0 {
		c.HandleTimeout = def.HandleTimeout
	}
	switch {
	case c.Prefetch < 1 || c.Prefetch > 1000:
		return c, errors.New("creditmsg: prefetch must be between 1 and 1000")
	case c.HandleTimeout < 0:
		return c, errors.New("creditmsg: handle timeout must be positive")
	}
	if err := c.ReconnectBackoff.Validate(); err != nil {
		return c, err
	}
	return c, c.RetryBackoff.Validate()
}

// Consumer reads order-service.credit-results and settles each delivery as
// its Handler says: ack after the work commits, requeue on a transient
// failure, reject (dead-letter) a message that breaks the contract.
//
// Deliveries are handled one at a time, in order. The topology is declared
// on every (re)connect with the same arguments as the publisher's.
type Consumer struct {
	url     string
	handler *Handler
	logger  *slog.Logger
	cfg     Config
	running atomic.Bool

	mu   sync.Mutex
	conn *amqp.Connection
}

// NewConsumer validates configuration; Run opens the connection.
func NewConsumer(url string, handler *Handler, logger *slog.Logger, cfg Config) (*Consumer, error) {
	if url == "" {
		return nil, errors.New("creditmsg: AMQP URL is required")
	}
	if _, err := amqp.ParseURI(url); err != nil {
		return nil, errors.New("creditmsg: invalid AMQP URL")
	}
	if handler == nil || logger == nil {
		return nil, errors.New("creditmsg: consumer dependencies are required")
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

// Metrics returns the per-result counters.
func (c *Consumer) Metrics() Metrics { return c.handler.Metrics() }

// Run consumes until ctx is cancelled, then returns ctx's error. A lost or
// failed session is retried with backoff. On cancellation the delivery
// being handled is finished and settled; prefetched deliveries go back to
// the queue when the connection closes.
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
		c.logger.WarnContext(ctx, "credit result consumer disconnected; reconnecting",
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
		Properties: amqp.Table{"connection_name": "order-service credit-results"},
	})
	if err != nil {
		// Dial errors can contain the URI, including credentials.
		return false, fmt.Errorf("%w: connection failed", messaging.ErrBrokerUnavailable)
	}
	defer func() { _ = conn.CloseDeadline(time.Now().Add(time.Second)) }()

	ch, err := conn.Channel()
	if err == nil {
		err = messaging.DeclareTopology(ch, false)
	}
	if err == nil {
		err = ch.Qos(c.cfg.Prefetch, 0, false)
	}
	var deliveries <-chan amqp.Delivery
	if err == nil {
		deliveries, err = ch.Consume(errand.QueueCreditResults, "", false, false, false, false, nil)
	}
	if err != nil {
		return false, fmt.Errorf("%w: setup failed: %w", messaging.ErrBrokerUnavailable, err)
	}

	c.setConn(conn)
	c.running.Store(true)
	defer func() {
		c.running.Store(false)
		c.setConn(nil)
	}()
	c.logger.InfoContext(ctx, "consuming credit results", slog.String("queue", errand.QueueCreditResults), slog.Int("prefetch", c.cfg.Prefetch))

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
		c.logger.WarnContext(ctx, "credit result not settled; the broker will redeliver it",
			slog.String("routing_key", d.RoutingKey), slog.String("message_id", d.MessageId),
			slog.String("disposition", disposition.String()), slog.Any("error", err))
	}
}

// handle runs the handler with its own timeout. The work is not cancelled
// by shutdown, so a delivery that started is finished and settled. A panic
// requeues the delivery; the delivery limit dead-letters one that always
// panics.
func (c *Consumer) handle(ctx context.Context, d amqp.Delivery) (disposition Disposition) {
	defer func() {
		if p := recover(); p != nil {
			c.logger.ErrorContext(ctx, "credit result handler panicked",
				slog.String("routing_key", d.RoutingKey), slog.String("message_id", d.MessageId), slog.Any("panic", p))
			disposition = Requeue
		}
	}()
	work, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.cfg.HandleTimeout)
	defer cancel()
	return c.handler.Handle(work, d.RoutingKey, d.Body)
}

func (c *Consumer) setConn(conn *amqp.Connection) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.conn = conn
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
