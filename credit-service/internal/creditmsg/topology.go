package creditmsg

import (
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Broker names, as in docs/asyncapi.yaml and order-service's contracts.
const (
	ExchangeCommands = "foc.commands"
	ExchangeEvents   = "foc.events"

	// QueueCommands is owned and declared by credit-service. Commands that
	// are rejected, or fail too many times, go to QueueCommandsDLQ.
	QueueCommands    = "credit-service.commands"
	QueueCommandsDLQ = "credit-service.commands.dlq"

	// ExchangeCommandsRetry and QueueCommandsRetry hold a command that failed
	// for a temporary reason while it waits to be tried again. The queue has no
	// consumer: its messages expire after retryDelay and are dead-lettered to
	// ExchangeCommands with the routing key they were published with, which
	// puts them back on QueueCommands. A command waits there instead of
	// blocking the consumer in a sleep.
	ExchangeCommandsRetry = "foc.commands.retry"
	QueueCommandsRetry    = "credit-service.commands.retry"

	// DefaultMaxAttempts is how many times the consumer tries a command that
	// keeps failing for a temporary reason before it dead-letters it. With the
	// 30 second retry delay that is about an hour, which covers a database
	// failover or a maintenance window.
	DefaultMaxAttempts = 120

	// DeliveryLimit is the queue's x-delivery-limit. It is only a backstop for
	// a delivery that is lost with its channel, because RabbitMQ 4 does not
	// count a message that is requeued with basic.nack against it, and the
	// consumer's retries are new messages anyway. The consumer enforces
	// DefaultMaxAttempts itself.
	DeliveryLimit = 5
)

// retryDelay is how long a failed command waits on QueueCommandsRetry. It is a
// queue argument, and RabbitMQ refuses to redeclare a queue with another
// value, so it is not configurable; tests shorten it with SetRetryDelay in
// export_test.go.
var retryDelay = 30 * time.Second

// CommandKeys lists the routing keys bound to QueueCommands.
func CommandKeys() []string {
	return []string{CommandReserve, CommandAdjust, CommandRelease, CommandTransfer}
}

// DeclareTopology declares the exchanges, queues and bindings credit-service
// needs. It is safe to call on every (re)connect. The publisher and the
// consumer must use it as it is, because RabbitMQ refuses to redeclare a queue
// with other arguments. The queue of results, order-service.credit-results, is
// order-service's and is never declared here.
func DeclareTopology(ch *amqp.Channel) error {
	for name, kind := range map[string]string{ExchangeCommands: "direct", ExchangeEvents: "topic", ExchangeCommandsRetry: "direct"} {
		if err := ch.ExchangeDeclare(name, kind, true, false, false, false, nil); err != nil {
			return err
		}
	}
	if _, err := ch.QueueDeclare(QueueCommandsDLQ, true, false, false, false, amqp.Table{"x-queue-type": "quorum"}); err != nil {
		return err
	}
	if _, err := ch.QueueDeclare(QueueCommands, true, false, false, false, amqp.Table{
		"x-queue-type":              "quorum",
		"x-delivery-limit":          int32(DeliveryLimit),
		"x-dead-letter-exchange":    "",
		"x-dead-letter-routing-key": QueueCommandsDLQ,
	}); err != nil {
		return err
	}
	for _, key := range CommandKeys() {
		if err := ch.QueueBind(QueueCommands, key, ExchangeCommands, false, nil); err != nil {
			return err
		}
	}
	// No dead-letter routing key, so an expired command keeps the routing key
	// it was published with and reaches QueueCommands through its binding.
	if _, err := ch.QueueDeclare(QueueCommandsRetry, true, false, false, false, amqp.Table{
		"x-queue-type":           "quorum",
		"x-message-ttl":          int32(retryDelay.Milliseconds()),
		"x-dead-letter-exchange": ExchangeCommands,
	}); err != nil {
		return err
	}
	for _, key := range CommandKeys() {
		if err := ch.QueueBind(QueueCommandsRetry, key, ExchangeCommandsRetry, false, nil); err != nil {
			return err
		}
	}
	return nil
}
