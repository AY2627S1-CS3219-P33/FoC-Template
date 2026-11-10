package creditmsg

import (
	"context"
	"errors"
	"strings"

	amqp "github.com/rabbitmq/amqp091-go"
)

// retryCountHeader counts how often the consumer has parked a command on
// QueueCommandsRetry. A parked command is a new message, so the broker's own
// x-acquired-count starts again at zero; this header carries the history.
const retryCountHeader = "retry-count"

var errRetryUnroutable = errors.New("creditmsg: retry exchange has no queue bound for the command")

// retryPublisher parks a command that failed for a temporary reason on
// QueueCommandsRetry. It owns one confirm-mode channel and is used by one
// delivery at a time, so a basic.return belongs to the publish that just
// finished.
type retryPublisher struct {
	ch      *amqp.Channel
	returns chan amqp.Return
	closing chan *amqp.Error
}

func newRetryPublisher(conn *amqp.Connection) (*retryPublisher, error) {
	ch, err := conn.Channel()
	if err != nil {
		return nil, err
	}
	if err := ch.Confirm(false); err != nil {
		_ = ch.Close()
		return nil, err
	}
	return &retryPublisher{
		ch:      ch,
		returns: ch.NotifyReturn(make(chan amqp.Return, 1)),
		closing: ch.NotifyClose(make(chan *amqp.Error, 1)),
	}, nil
}

// publish sends a copy of d to the retry queue, with retries as its retry
// count, and returns once the broker has confirmed it. Only then may the
// caller ack d: acking first could lose the command, and a crash in between
// leaves a duplicate, which credit-service answers from the stored result.
func (r *retryPublisher) publish(ctx context.Context, d amqp.Delivery, retries int) error {
	confirmation, err := r.ch.PublishWithDeferredConfirmWithContext(ctx, ExchangeCommandsRetry, d.RoutingKey, true, false,
		retryPublishing(d, retries))
	if err != nil {
		return err
	}
	ack, err := confirmation.WaitContext(ctx)
	if err != nil {
		return err
	}
	// RabbitMQ sends basic.return before basic.ack and the client dispatches
	// both on one reader, so a return for this message is buffered by now.
	select {
	case <-r.returns:
		return errRetryUnroutable
	default:
	}
	if !ack {
		return errors.New("creditmsg: broker did not confirm the retry message")
	}
	return nil
}

// retryPublishing copies d for the retry queue: the same body, identity and
// routing key, persistent, with the retry count raised to retries. Headers the
// broker manages (x-acquired-count, x-death and the like) are not copied; the
// broker adds its own when the copy is dead-lettered back.
func retryPublishing(d amqp.Delivery, retries int) amqp.Publishing {
	headers := amqp.Table{}
	for key, value := range d.Headers {
		if !strings.HasPrefix(key, "x-") {
			headers[key] = value
		}
	}
	headers[retryCountHeader] = int32(retries)
	return amqp.Publishing{
		Headers:         headers,
		ContentType:     d.ContentType,
		ContentEncoding: d.ContentEncoding,
		DeliveryMode:    amqp.Persistent,
		Priority:        d.Priority,
		CorrelationId:   d.CorrelationId,
		ReplyTo:         d.ReplyTo,
		MessageId:       d.MessageId,
		Timestamp:       d.Timestamp,
		Type:            d.Type,
		AppId:           d.AppId,
		Body:            d.Body,
	}
}

// retryCount is how often d has been parked on the retry queue before.
func retryCount(d amqp.Delivery) int { return headerInt(d.Headers[retryCountHeader]) }

// attempt returns which attempt this delivery is, counting from 1: the times
// it has been parked, plus the deliveries of this copy that the broker counted
// in x-acquired-count (a requeue, or a delivery lost with its channel), plus
// this one. The headers are kept by the broker, so the count survives a
// restart and is shared by replicas.
func attempt(d amqp.Delivery) int {
	return retryCount(d) + headerInt(d.Headers["x-acquired-count"]) + 1
}

func headerInt(v any) int {
	switch n := v.(type) {
	case int64:
		return int(n)
	case int32:
		return int(n)
	case int:
		return n
	}
	return 0
}
