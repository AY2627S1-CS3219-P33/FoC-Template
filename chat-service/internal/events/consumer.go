// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-10
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

package events

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Backoff is an exponential delay: Base, 2×Base, 4×Base, … up to Cap.
type Backoff struct {
	Base time.Duration
	Cap  time.Duration
}

// Delay is the wait before attempt n (1 for the first retry).
func (b Backoff) Delay(n int) time.Duration {
	delay := b.Base
	for i := 1; i < n && delay < b.Cap; i++ {
		delay *= 2
	}
	return min(delay, b.Cap)
}

// MaxAttempts is how many times an event failing with an unexpected error
// (RetryLimited) is tried before it is dead-lettered.
const MaxAttempts = 5

// maxTrackedEvents bounds the attempt counts kept in memory.
const maxTrackedEvents = 10_000

// Config tunes the consumer. Zero fields take the defaults.
type Config struct {
	// Prefetch bounds the deliveries RabbitMQ sends ahead of acks.
	Prefetch int
	// ReconnectBackoff spaces out reconnects after a lost connection.
	ReconnectBackoff Backoff
	// RetryBackoff is the wait before requeueing after a temporary failure,
	// growing with consecutive failures so a database outage is not hammered.
	RetryBackoff Backoff
	// HandleTimeout bounds the database work for one event.
	HandleTimeout time.Duration
}

func (c Config) withDefaults() Config {
	if c.Prefetch == 0 {
		c.Prefetch = 10
	}
	if c.ReconnectBackoff == (Backoff{}) {
		c.ReconnectBackoff = Backoff{Base: 500 * time.Millisecond, Cap: 30 * time.Second}
	}
	if c.RetryBackoff == (Backoff{}) {
		c.RetryBackoff = Backoff{Base: time.Second, Cap: 30 * time.Second}
	}
	if c.HandleTimeout == 0 {
		c.HandleTimeout = 30 * time.Second
	}
	return c
}

// Consumer reads Queue and settles each delivery as the Handler decides:
// ack after the change commits, requeue on a temporary failure, reject
// (dead-letter) an event that breaks the contract. It reconnects with
// backoff whenever the connection is lost, and declares the topology on
// every connect.
type Consumer struct {
	url     string
	handler *Handler
	logger  *slog.Logger
	cfg     Config
	running atomic.Bool
	// attempts counts RetryLimited failures per event, because RabbitMQ 4
	// does not count explicit requeues. It lives in this process only: after
	// a restart, or on another copy, counting starts again, which only
	// delays dead-lettering. Only the consumer goroutine touches it.
	attempts map[string]int
}

// NewConsumer checks its arguments; Run connects.
func NewConsumer(url string, handler *Handler, logger *slog.Logger, cfg Config) (*Consumer, error) {
	if _, err := amqp.ParseURI(url); err != nil {
		// The parse error can contain the URL, which holds a password.
		return nil, errors.New("events: invalid AMQP URL")
	}
	if handler == nil || logger == nil {
		return nil, errors.New("events: handler and logger are required")
	}
	cfg = cfg.withDefaults()
	if cfg.Prefetch < 1 || cfg.Prefetch > 1000 || cfg.HandleTimeout < 0 {
		return nil, errors.New("events: prefetch must be 1 to 1000 and the handle timeout positive")
	}
	return &Consumer{url: url, handler: handler, logger: logger, cfg: cfg, attempts: map[string]int{}}, nil
}

// Running reports whether the consumer is subscribed to Queue, for /readyz.
func (c *Consumer) Running() bool { return c.running.Load() }

// Run consumes until ctx is cancelled, then returns ctx's error. On
// cancellation the event being handled is finished and settled; prefetched
// events go back to the queue when the connection closes.
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
		delay := c.cfg.ReconnectBackoff.Delay(failures)
		c.logger.WarnContext(ctx, "errand event consumer disconnected; reconnecting",
			"error", err.Error(), "retryIn", delay.String(), "failures", failures)
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
		Properties: amqp.Table{"connection_name": "chat-service errand-events"},
	})
	if err != nil {
		// Dial errors can contain the URL, including the password.
		return false, errors.New("connection to RabbitMQ failed")
	}
	defer func() { _ = conn.CloseDeadline(time.Now().Add(time.Second)) }()

	ch, err := conn.Channel()
	if err == nil {
		err = Declare(ch)
	}
	if err == nil {
		err = ch.Qos(c.cfg.Prefetch, 0, false)
	}
	var deliveries <-chan amqp.Delivery
	if err == nil {
		deliveries, err = ch.Consume(Queue, "", false, false, false, false, nil)
	}
	if err != nil {
		return false, fmt.Errorf("RabbitMQ set-up failed: %w", err)
	}

	c.running.Store(true)
	defer c.running.Store(false)
	c.logger.InfoContext(ctx, "consuming errand events",
		"queue", Queue, "bindings", Bindings(), "prefetch", c.cfg.Prefetch)

	retries := 0
	for {
		select {
		case <-ctx.Done():
			return true, ctx.Err()
		case d, ok := <-deliveries:
			if !ok {
				return true, errors.New("connection to RabbitMQ lost")
			}
			c.settle(ctx, d, &retries)
		}
	}
}

// settle handles one delivery, then acks, requeues or rejects it. retries
// counts consecutive failures, for the retry backoff.
func (c *Consumer) settle(ctx context.Context, d amqp.Delivery, retries *int) {
	disposition := c.handle(ctx, d)
	key := eventKey(d)
	if disposition == RetryLimited {
		c.attempts[key]++
		if c.attempts[key] >= MaxAttempts {
			c.logger.ErrorContext(ctx, "errand event dead-lettered after repeated unexpected failures",
				"routingKey", d.RoutingKey, "messageId", d.MessageId, "attempts", c.attempts[key])
			disposition = Reject
		}
	}
	var err error
	switch disposition {
	case Ack, Reject:
		*retries = 0
		delete(c.attempts, key)
		if disposition == Ack {
			err = d.Ack(false)
		} else {
			err = d.Nack(false, false) // not requeued: dead-lettered
		}
	default:
		*retries++
		if len(c.attempts) > maxTrackedEvents {
			clear(c.attempts) // never grow without bound; counting restarts
		}
		// Shutdown cuts the wait short; the event is requeued at once.
		_ = sleep(ctx, c.cfg.RetryBackoff.Delay(*retries))
		err = d.Nack(false, true)
	}
	if err != nil {
		c.logger.WarnContext(ctx, "errand event not settled; RabbitMQ will deliver it again",
			"routingKey", d.RoutingKey, "messageId", d.MessageId,
			"disposition", disposition.String(), "error", err.Error())
	}
}

// handle runs the handler with its own timeout. Shutdown does not cancel the
// work, so an event that started is finished and settled. A panic counts as
// an unexpected failure, so an event that always panics is dead-lettered.
func (c *Consumer) handle(ctx context.Context, d amqp.Delivery) (disposition Disposition) {
	defer func() {
		if p := recover(); p != nil {
			c.logger.ErrorContext(ctx, "errand event handler panicked",
				"routingKey", d.RoutingKey, "messageId", d.MessageId, "panic", fmt.Sprint(p))
			disposition = RetryLimited
		}
	}()
	work, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.cfg.HandleTimeout)
	defer cancel()
	return c.handler.Handle(work, d.RoutingKey, d.MessageId, d.Body)
}

// eventKey identifies a delivery for attempt counting: the AMQP message ID,
// which order-service always sets, or else a hash of the body.
func eventKey(d amqp.Delivery) string {
	if d.MessageId != "" {
		return "id:" + d.MessageId
	}
	sum := sha256.Sum256(d.Body)
	return "body:" + hex.EncodeToString(sum[:])
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
