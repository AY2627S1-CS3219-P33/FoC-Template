package creditmsg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"slices"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/wallet"
)

var (
	// ErrUnroutable means the broker accepted the message but no queue is bound
	// for it, for example because order-service has not declared its results
	// queue yet. It is retryable.
	ErrUnroutable = errors.New("creditmsg: no queue matched the message")
	// ErrBrokerUnavailable means RabbitMQ could not be reached. It is
	// retryable and never carries the AMQP URL.
	ErrBrokerUnavailable = errors.New("creditmsg: RabbitMQ unavailable")
	// ErrUnpublishable means the message breaks the contract, so no retry can
	// help.
	ErrUnpublishable = errors.New("creditmsg: message cannot be published")

	errClosed = errors.New("creditmsg: publisher is closed")
)

const publishTimeout = 10 * time.Second

// ResultKeys lists every result routing key, published on foc.events.
func ResultKeys() []string {
	return []string{
		wallet.ResultReserved, wallet.ResultReservationRefused, wallet.ResultAdjusted, wallet.ResultAdjustRefused,
		wallet.ResultReleased, wallet.ResultTransferred, wallet.ResultOperationFailed,
	}
}

// Publisher publishes result messages on foc.events. It owns one confirm-mode
// channel and has one publish in flight, so a returned (unroutable) message
// can be matched to its publish without ambiguity. A lost session reconnects
// on the next Publish. After a failed reconnect, further publishes fail fast
// with ErrBrokerUnavailable until the backoff has passed, so an outage does
// not cost every outbox row a dial; the outbox owns the per-row retries.
type Publisher struct {
	url    string
	gate   chan struct{}
	closed bool

	conn    *amqp.Connection
	channel *amqp.Channel
	socket  net.Conn
	returns <-chan amqp.Return

	backoff  Backoff
	failures int
	retryAt  time.Time
}

// NewPublisher validates configuration; the first publish opens the connection.
func NewPublisher(url string) (*Publisher, error) {
	if url == "" {
		return nil, errors.New("creditmsg: AMQP URL is required")
	}
	if _, err := amqp.ParseURI(url); err != nil {
		return nil, errors.New("creditmsg: invalid AMQP URL")
	}
	return &Publisher{
		url:     url,
		gate:    make(chan struct{}, 1),
		backoff: Backoff{Base: 500 * time.Millisecond, Cap: 30 * time.Second},
	}, nil
}

// Publish sends one outbox payload, which must be a result Envelope, with the
// given routing key, and returns once the broker has confirmed it. Errors are
// ErrUnpublishable (never retry), ErrUnroutable and ErrBrokerUnavailable, or
// a timeout or cancellation; all of the others are worth retrying.
func (p *Publisher) Publish(ctx context.Context, routingKey string, payload []byte) (err error) {
	env, err := checkResult(routingKey, payload)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, publishTimeout)
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

	// Closing the socket interrupts AMQP calls and writes, which do not all
	// honour a context.
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

	confirmation, err := p.channel.PublishWithDeferredConfirmWithContext(ctx, ExchangeEvents, routingKey, true, false,
		amqp.Publishing{
			DeliveryMode:  amqp.Persistent,
			ContentType:   "application/json",
			MessageId:     env.MessageID,
			CorrelationId: env.CorrelationID,
			Type:          routingKey,
			Timestamp:     env.OccurredAt,
			Body:          payload,
		})
	if err != nil {
		return fmt.Errorf("%w: publish failed", ErrBrokerUnavailable)
	}
	ack, err := confirmation.WaitContext(ctx)
	if err != nil {
		return err
	}
	// RabbitMQ sends basic.return before basic.ack and the client dispatches
	// both on one reader, so a return for this message is buffered by now.
	select {
	case returned, ok := <-p.returns:
		if !ok {
			return fmt.Errorf("%w: channel closed", ErrBrokerUnavailable)
		}
		return fmt.Errorf("%w (reply %d)", ErrUnroutable, returned.ReplyCode)
	default:
	}
	if !ack {
		return fmt.Errorf("%w: the broker did not confirm", ErrBrokerUnavailable)
	}
	return nil
}

// checkResult decides, before any network call, whether a payload is a result
// message that can be published.
func checkResult(routingKey string, payload []byte) (Envelope, error) {
	var env Envelope
	data := bytes.TrimSpace(payload)
	if err := json.Unmarshal(data, &env); err != nil {
		return Envelope{}, fmt.Errorf("%w: payload is not an envelope", ErrUnpublishable)
	}
	body := bytes.TrimSpace(env.Data)
	switch {
	case !slices.Contains(ResultKeys(), routingKey), env.Type != routingKey:
		return Envelope{}, fmt.Errorf("%w: unknown result type", ErrUnpublishable)
	case env.MessageID == "" || len(env.MessageID) > 255,
		env.CorrelationID == "" || len(env.CorrelationID) > 255:
		return Envelope{}, fmt.Errorf("%w: message id or correlation id is missing or too long", ErrUnpublishable)
	case env.ErrandID == "", env.ErrandVersion < 1, env.OccurredAt.IsZero():
		return Envelope{}, fmt.Errorf("%w: errand or time is missing", ErrUnpublishable)
	case len(body) == 0 || body[0] != '{':
		return Envelope{}, fmt.Errorf("%w: data must be an object", ErrUnpublishable)
	}
	return env, nil
}

func (p *Publisher) connected() bool {
	return p.conn != nil && !p.conn.IsClosed() && p.channel != nil && !p.channel.IsClosed()
}

// reconnect replaces a lost session unless the backoff after a recent failure
// has not passed. The caller holds the gate.
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
			p.retryAt = time.Now().Add(p.backoff.delay(p.failures))
		}
		return nil, err
	}
	p.failures, p.retryAt = 0, time.Time{}
	return stop, nil
}

func (p *Publisher) connect(ctx context.Context) (func() bool, error) {
	var stop func() bool
	conn, err := amqp.DialConfig(p.url, amqp.Config{
		Properties: amqp.Table{"connection_name": "credit-service results"},
		Dial: func(network, address string) (net.Conn, error) {
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
		err = DeclareTopology(channel)
	}
	if err == nil {
		err = channel.Confirm(false)
	}
	if err != nil {
		stop()
		p.disconnect()
		return nil, fmt.Errorf("%w: setup failed", ErrBrokerUnavailable)
	}
	p.returns = channel.NotifyReturn(make(chan amqp.Return, 1))
	return stop, nil
}

func (p *Publisher) disconnect() {
	if p.socket != nil {
		_ = p.socket.Close()
	}
	p.socket, p.conn, p.channel, p.returns = nil, nil, nil, nil
}

// Close ends the session. Later publishes fail.
func (p *Publisher) Close() error {
	p.gate <- struct{}{}
	defer func() { <-p.gate }()
	p.closed = true
	p.disconnect()
	return nil
}
