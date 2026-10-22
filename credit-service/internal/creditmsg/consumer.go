package creditmsg

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync/atomic"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

var errConnectionLost = errors.New("creditmsg: broker connection lost")

// MessageHandler processes one command message. Handler implements it.
type MessageHandler interface {
	Handle(ctx context.Context, routingKey string, body []byte) error
}

// Backoff is exponential backoff with full jitter: the delay before attempt n
// (1-based) is drawn uniformly from [0, min(Cap, Base*2^(n-1))].
type Backoff struct {
	Base time.Duration
	Cap  time.Duration
}

func (b Backoff) delay(attempt int) time.Duration {
	ceiling := b.Base
	for i := 1; i < attempt && ceiling < b.Cap; i++ {
		ceiling *= 2
	}
	ceiling = min(ceiling, b.Cap)
	if ceiling <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(ceiling) + 1))
}

// Config tunes the consumer. Zero fields take the defaults from DefaultConfig.
type Config struct {
	// Prefetch bounds the unacknowledged deliveries the broker sends ahead.
	Prefetch int
	// ReconnectBackoff spaces out reconnects after a lost or failed session.
	ReconnectBackoff Backoff
	// RetryBackoff is the wait before a message is requeued after a temporary
	// failure. It grows with consecutive failures.
	RetryBackoff Backoff
	// MaxAttempts is how often a message is tried before it is dead-lettered
	// after temporary failures.
	MaxAttempts int
	// HandleTimeout bounds the work for one delivery.
	HandleTimeout time.Duration
}

// DefaultConfig returns 5 attempts, prefetch 16, reconnect backoff 500 ms to 30 s, retry
// backoff 1 s to 30 s and a 30 s handle timeout.
func DefaultConfig() Config {
	return Config{
		Prefetch:         16,
		ReconnectBackoff: Backoff{Base: 500 * time.Millisecond, Cap: 30 * time.Second},
		RetryBackoff:     Backoff{Base: time.Second, Cap: 30 * time.Second},
		MaxAttempts:      DeliveryLimit,
		HandleTimeout:    30 * time.Second,
	}
}

func (c Config) withDefaults() (Config, error) {
	def := DefaultConfig()
	if c.Prefetch == 0 {
		c.Prefetch = def.Prefetch
	}
	if c.ReconnectBackoff == (Backoff{}) {
		c.ReconnectBackoff = def.ReconnectBackoff
	}
	if c.RetryBackoff == (Backoff{}) {
		c.RetryBackoff = def.RetryBackoff
	}
	if c.MaxAttempts == 0 {
		c.MaxAttempts = def.MaxAttempts
	}
	if c.HandleTimeout == 0 {
		c.HandleTimeout = def.HandleTimeout
	}
	switch {
	case c.MaxAttempts < 1:
		return c, errors.New("creditmsg: max attempts must be at least 1")
	case c.Prefetch < 1 || c.Prefetch > 1000:
		return c, errors.New("creditmsg: prefetch must be between 1 and 1000")
	case c.HandleTimeout < 0:
		return c, errors.New("creditmsg: handle timeout must be positive")
	case c.ReconnectBackoff.Base <= 0 || c.ReconnectBackoff.Cap < c.ReconnectBackoff.Base,
		c.RetryBackoff.Base <= 0 || c.RetryBackoff.Cap < c.RetryBackoff.Base:
		return c, errors.New("creditmsg: backoff base must be positive and at most the cap")
	}
	return c, nil
}

// Consumer reads credit-service.commands and settles each delivery as the
// handler's result says: ack after the work has committed, requeue after a
// temporary failure, reject (dead-letter) a message that can never succeed.
//
// Deliveries are handled one at a time. The topology is declared on every
// (re)connect.
type Consumer struct {
	url     string
	handler MessageHandler
	logger  *slog.Logger
	cfg     Config
	running atomic.Bool
}

// NewConsumer validates configuration; Run opens the connection.
func NewConsumer(url string, handler MessageHandler, logger *slog.Logger, cfg Config) (*Consumer, error) {
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
		delay := c.cfg.ReconnectBackoff.delay(failures)
		c.logger.WarnContext(ctx, "credit command consumer disconnected; reconnecting",
			slog.Any("error", err), slog.Duration("retry_in", delay), slog.Int("failures", failures))
		if err := sleep(ctx, delay); err != nil {
			return err
		}
	}
}

// session runs one connection until it is lost or ctx is cancelled. It reports
// whether it got as far as subscribing.
func (c *Consumer) session(ctx context.Context) (bool, error) {
	conn, err := amqp.DialConfig(c.url, amqp.Config{
		Dial:       amqp.DefaultDial(10 * time.Second),
		Properties: amqp.Table{"connection_name": "credit-service commands"},
	})
	if err != nil {
		// Dial errors can contain the URI, including credentials.
		return false, errors.New("connection to the broker failed")
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
		deliveries, err = ch.Consume(QueueCommands, "", false, false, false, false, nil)
	}
	if err != nil {
		return false, fmt.Errorf("broker setup failed: %w", err)
	}

	c.running.Store(true)
	defer c.running.Store(false)
	c.logger.InfoContext(ctx, "consuming credit commands", slog.String("queue", QueueCommands), slog.Int("prefetch", c.cfg.Prefetch))

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

// settle handles one delivery and acks, requeues or rejects it. retries counts
// consecutive temporary failures, for the retry backoff.
func (c *Consumer) settle(ctx context.Context, d amqp.Delivery, retries *int) {
	err := c.handle(ctx, d)
	disposition := DispositionOf(err)
	var settleErr error
	switch {
	case disposition == Ack:
		*retries = 0
		settleErr = d.Ack(false)
	case disposition == Reject:
		*retries = 0
		c.logger.WarnContext(ctx, "credit command rejected",
			slog.String("routing_key", d.RoutingKey), slog.String("message_id", d.MessageId), slog.Any("error", err))
		settleErr = d.Nack(false, false)
	case attempt(d) >= c.cfg.MaxAttempts:
		// Order-service sends a command again if it gets no result, and
		// credit-service answers a repeat, so giving up here loses nothing.
		*retries = 0
		c.logger.ErrorContext(ctx, "credit command failed too often; dead-lettering",
			slog.String("routing_key", d.RoutingKey), slog.String("message_id", d.MessageId),
			slog.Int("attempts", attempt(d)), slog.Any("error", err))
		settleErr = d.Nack(false, false)
	default:
		*retries++
		c.logger.WarnContext(ctx, "credit command failed; requeueing",
			slog.String("routing_key", d.RoutingKey), slog.String("message_id", d.MessageId),
			slog.Int("consecutive_failures", *retries), slog.Any("error", err))
		// A cancelled wait still requeues at once, for another replica.
		_ = sleep(ctx, c.cfg.RetryBackoff.delay(*retries))
		settleErr = d.Nack(false, true)
	}
	if settleErr != nil {
		c.logger.WarnContext(ctx, "credit command not settled; the broker will redeliver it",
			slog.String("routing_key", d.RoutingKey), slog.String("message_id", d.MessageId), slog.Any("error", settleErr))
	}
}

// attempt returns which attempt this delivery is, counting from 1. The broker
// counts the earlier deliveries in the x-acquired-count header, which survives
// a restart and is the same for every replica.
func attempt(d amqp.Delivery) int {
	switch n := d.Headers["x-acquired-count"].(type) {
	case int64:
		return int(n) + 1
	case int32:
		return int(n) + 1
	case int:
		return n + 1
	}
	return 1
}

// handle runs the handler with its own timeout. The work is not cancelled by
// shutdown, so a delivery that started is finished and settled. A panic counts
// as a temporary failure; the delivery limit dead-letters a message that
// always panics.
func (c *Consumer) handle(ctx context.Context, d amqp.Delivery) (err error) {
	defer func() {
		if p := recover(); p != nil {
			c.logger.ErrorContext(ctx, "credit command handler panicked",
				slog.String("routing_key", d.RoutingKey), slog.String("message_id", d.MessageId), slog.Any("panic", p))
			err = fmt.Errorf("handler panicked: %v", p)
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
