package creditmsg

import (
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

	// DeliveryLimit is the number of attempts a command gets. The consumer
	// enforces it (see Consumer), because RabbitMQ 4 does not count a message
	// that the consumer requeues with basic.nack against x-delivery-limit. The
	// queue argument stays as a backstop for deliveries that are lost with
	// their channel.
	DeliveryLimit = 5
)

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
	for name, kind := range map[string]string{ExchangeCommands: "direct", ExchangeEvents: "topic"} {
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
	return nil
}
