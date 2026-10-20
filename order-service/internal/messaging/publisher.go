// Package messaging publishes transactional outbox messages to RabbitMQ.
package messaging

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"slices"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/resilience"
	amqp "github.com/rabbitmq/amqp091-go"
)

var (
	ErrUnroutable = errors.New("messaging: no queue matched the message")
	// ErrBrokerUnavailable marks a publish or ping that could not reach
	// RabbitMQ. It is retryable; it never carries the AMQP URL.
	ErrBrokerUnavailable = errors.New("messaging: RabbitMQ unavailable")
	errClosed            = errors.New("messaging: publisher is closed")
)

// Reconnect backoff after a failed dial or channel setup.
var defaultReconnectBackoff = resilience.BackoffPolicy{Base: 500 * time.Millisecond, Cap: 30 * time.Second}

// Publisher owns a single confirm-mode channel, with one in-flight publish so
// mandatory returns can be matched unambiguously. A lost session reconnects on
// the next Publish or Ping; after a failed reconnect, further attempts fail
// fast with ErrBrokerUnavailable until the backoff delay passes, so a broker
// outage does not cost every outbox row a dial. The outbox owns per-row retry
// scheduling.
type Publisher struct {
	url     string
	tap     bool
	gate    chan struct{}
	conn    *amqp.Connection
	channel *amqp.Channel
	socket  net.Conn
	returns <-chan amqp.Return
	closed  bool

	backoff  resilience.BackoffPolicy
	failures int
	retryAt  time.Time
}

var _ errand.Publisher = (*Publisher)(nil)

// NewPublisher validates configuration; the first publish opens the connection.
func NewPublisher(url string, declareTap bool) (*Publisher, error) {
	if url == "" {
		return nil, errors.New("messaging: AMQP URL is required")
	}
	if _, err := amqp.ParseURI(url); err != nil {
		return nil, errors.New("messaging: invalid AMQP URL")
	}
	return &Publisher{url: url, tap: declareTap, gate: make(chan struct{}, 1), backoff: defaultReconnectBackoff}, nil
}

func (p *Publisher) Publish(ctx context.Context, m errand.Message) (err error) {
	body, err := encode(m)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	select {
	case p.gate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-p.gate }()
	if p.closed {
		return errClosed
	}
	// Closing the socket interrupts AMQP RPCs and writes, which do not all honor context.
	var stop func() bool
	if !p.connected() {
		stop, err = p.reconnect(ctx)
		if err != nil {
			return err
		}
	} else {
		socket := p.socket
		stop = context.AfterFunc(ctx, func() { _ = socket.Close() })
	}
	defer stop()
	defer func() {
		if err != nil {
			p.disconnect()
		}
	}()
	confirmation, err := p.channel.PublishWithDeferredConfirmWithContext(ctx, m.Exchange, m.RoutingKey, true, false, amqp.Publishing{
		DeliveryMode: amqp.Persistent, ContentType: "application/json", MessageId: m.Envelope.MessageID,
		CorrelationId: m.Envelope.CorrelationID, Type: m.RoutingKey, Timestamp: m.Envelope.OccurredAt, Body: body,
	})
	if err != nil {
		return fmt.Errorf("messaging: publish failed: %w", err)
	}
	ack, err := confirmation.WaitContext(ctx)
	if err != nil {
		return err
	}
	// RabbitMQ sends basic.return before basic.ack, and the client dispatches
	// both on the same reader. The buffered return is available after WaitContext.
	select {
	case returned, ok := <-p.returns:
		if !ok {
			return errors.New("messaging: publish channel closed")
		}
		return fmt.Errorf("%w (reply %d)", ErrUnroutable, returned.ReplyCode)
	default:
	}
	if !ack {
		return errors.New("messaging: broker did not confirm publication")
	}
	return nil
}

func encode(m errand.Message) ([]byte, error) {
	validRoute := m.Exchange == errand.ExchangeCommands && slices.Contains(errand.CreditCommands(), m.RoutingKey)
	for _, name := range errand.EventNames() {
		if m.Exchange == errand.ExchangeEvents && m.RoutingKey == string(name) {
			validRoute = true
		}
	}
	e := m.Envelope
	data := bytes.TrimSpace(e.Data)
	if !validRoute || e.Type != m.RoutingKey || e.MessageID == "" || len(e.MessageID) > 255 || len(e.CorrelationID) > 255 || e.ErrandID == "" || e.ErrandVersion < 1 || e.OccurredAt.IsZero() || len(data) == 0 || data[0] != '{' || !json.Valid(data) {
		return nil, fmt.Errorf("%w: invalid outbound envelope or route", errand.ErrUnpublishable)
	}
	if m.Exchange == errand.ExchangeCommands {
		var command errand.CreditCommand
		if err := json.Unmarshal(data, &command); err != nil || command.ErrandID != e.ErrandID || command.ErrandVersion != e.ErrandVersion || command.RequesterID == "" {
			return nil, fmt.Errorf("%w: invalid credit command identity", errand.ErrUnpublishable)
		}
		switch m.RoutingKey {
		case errand.CommandCreditReserve, errand.CommandCreditTransfer:
			if command.Amount <= 0 || (m.RoutingKey == errand.CommandCreditTransfer && command.CourierID == "") {
				return nil, fmt.Errorf("%w: invalid credit amount or payee", errand.ErrUnpublishable)
			}
		case errand.CommandCreditAdjust:
			if command.NewAmount <= 0 || command.Seq <= 0 {
				return nil, fmt.Errorf("%w: invalid adjustment amount or sequence", errand.ErrUnpublishable)
			}
		}
	}
	body, err := json.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("%w: cannot encode envelope", errand.ErrUnpublishable)
	}
	return body, nil
}

func (p *Publisher) connect(ctx context.Context) (func() bool, error) {
	var stop func() bool
	conn, err := amqp.DialConfig(p.url, amqp.Config{Dial: func(network, address string) (net.Conn, error) {
		socket, err := (&net.Dialer{}).DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		p.socket = socket
		stop = context.AfterFunc(ctx, func() { _ = socket.Close() })
		return socket, nil
	}})
	if err != nil {
		if stop != nil {
			stop()
		}
		p.disconnect()
		// Dial errors can contain the URI, including credentials.
		return nil, fmt.Errorf("%w: connection failed", ErrBrokerUnavailable)
	}
	p.conn = conn
	channel, err := conn.Channel()
	if err == nil {
		p.channel = channel
		err = DeclareTopology(channel, p.tap)
	}
	if err == nil {
		err = channel.Confirm(false)
	}
	if err != nil {
		stop()
		p.disconnect()
		return nil, fmt.Errorf("%w: setup failed: %w", ErrBrokerUnavailable, err)
	}
	p.returns = channel.NotifyReturn(make(chan amqp.Return, 1))
	return stop, nil
}

// DeclareTopology idempotently declares order-service's side of the broker
// topology: both exchanges, the credit-results quorum queue and its DLQ, and
// with tap the events tap queue. The publisher and the credit-result consumer
// both call it on every (re)connect; the arguments must never differ between
// them, because RabbitMQ refuses to redeclare a queue with other arguments.
func DeclareTopology(ch *amqp.Channel, tap bool) error {
	for name, kind := range map[string]string{errand.ExchangeCommands: "direct", errand.ExchangeEvents: "topic"} {
		if err := ch.ExchangeDeclare(name, kind, true, false, false, false, nil); err != nil {
			return err
		}
	}
	if _, err := ch.QueueDeclare(errand.QueueCreditResultsDLQ, true, false, false, false, amqp.Table{"x-queue-type": "quorum"}); err != nil {
		return err
	}
	if _, err := ch.QueueDeclare(errand.QueueCreditResults, true, false, false, false, amqp.Table{
		"x-queue-type": "quorum", "x-delivery-limit": int32(5), "x-dead-letter-exchange": "", "x-dead-letter-routing-key": errand.QueueCreditResultsDLQ,
	}); err != nil {
		return err
	}
	if err := ch.QueueBind(errand.QueueCreditResults, errand.BindingCreditResults, errand.ExchangeEvents, false, nil); err != nil {
		return err
	}
	if tap {
		if _, err := ch.QueueDeclare(errand.QueueEventsTap, true, false, false, false, nil); err != nil {
			return err
		}
		return ch.QueueBind(errand.QueueEventsTap, errand.BindingErrandEvents, errand.ExchangeEvents, false, nil)
	}
	return nil
}

// Ping reports whether RabbitMQ is reachable, connecting if needed, for
// readiness checks. It honours the reconnect backoff.
func (p *Publisher) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	select {
	case p.gate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-p.gate }()
	if p.closed {
		return errClosed
	}
	if p.connected() {
		return nil
	}
	stop, err := p.reconnect(ctx)
	if err != nil {
		return err
	}
	stop()
	return nil
}

func (p *Publisher) connected() bool {
	return p.conn != nil && !p.conn.IsClosed() && p.channel != nil && !p.channel.IsClosed()
}

// reconnect replaces a lost session unless a recent failure's backoff has not
// passed. Caller holds the gate.
func (p *Publisher) reconnect(ctx context.Context) (func() bool, error) {
	if wait := time.Until(p.retryAt); wait > 0 {
		return nil, fmt.Errorf("%w: reconnecting in %s", ErrBrokerUnavailable, wait.Round(time.Millisecond))
	}
	p.disconnect()
	stop, err := p.connect(ctx)
	if err != nil {
		// A cancelled caller says nothing about the broker.
		if ctx.Err() == nil {
			p.failures++
			p.retryAt = time.Now().Add(p.backoff.Backoff(p.failures))
		}
		return nil, err
	}
	p.failures, p.retryAt = 0, time.Time{}
	return stop, nil
}

func (p *Publisher) disconnect() {
	if p.socket != nil {
		_ = p.socket.Close()
	}
	p.socket = nil
	p.conn = nil
	p.channel = nil
	p.returns = nil
}

func (p *Publisher) Close() error {
	p.gate <- struct{}{}
	defer func() { <-p.gate }()
	p.closed = true
	p.disconnect()
	return nil
}
