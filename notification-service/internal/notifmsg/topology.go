// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: Implementation of notification-service notification event messaging.
// Author review: I validated correctness, edited for style, and added boundary checks.

package notifmsg

import (
	amqp "github.com/rabbitmq/amqp091-go"
)

// Broker names, as in docs/asyncapi.yaml. notification-service owns a single
// queue bound to every errand event on the shared topic exchange. It is only
// a consumer: it declares no command exchange and publishes nothing.
const (
	// ExchangeEvents is the durable topic exchange order-service publishes
	// errand events (and credit-service publishes results) to.
	ExchangeEvents = "foc.events"

	// QueueErrands is owned and declared by notification-service. Events that
	// are rejected, or fail too many times, go to QueueErrandsDLQ.
	QueueErrands    = "notification-service.errands"
	QueueErrandsDLQ = "notification-service.errands.dlq"

	// ErrandKey binds QueueErrands to every errand.* routing key. The contract
	// allows errand.# or a subset; the service takes them all and decides which
	// ones become notifications in the handler.
	ErrandKey = "errand.#"

	// DeliveryLimit is the number of attempts an event gets before it is
	// dead-lettered. The consumer enforces it (RabbitMQ 4 does not count a
	// basic.nack-requeue against x-delivery-limit); the queue argument stays as
	// a backstop for deliveries lost with their channel.
	DeliveryLimit = 5
)

// DeclareTopology declares the exchange, the queue, its dead-letter queue and
// the errand.# binding. It is safe to call on every (re)connect. order-service
// owns and declares the exchange too; both declare it the same way, which
// RabbitMQ requires.
func DeclareTopology(ch *amqp.Channel) error {
	if err := ch.ExchangeDeclare(ExchangeEvents, "topic", true, false, false, false, nil); err != nil {
		return err
	}
	if _, err := ch.QueueDeclare(QueueErrandsDLQ, true, false, false, false, amqp.Table{"x-queue-type": "quorum"}); err != nil {
		return err
	}
	if _, err := ch.QueueDeclare(QueueErrands, true, false, false, false, amqp.Table{
		"x-queue-type":              "quorum",
		"x-delivery-limit":          int32(DeliveryLimit),
		"x-dead-letter-exchange":    "",
		"x-dead-letter-routing-key": QueueErrandsDLQ,
	}); err != nil {
		return err
	}
	return ch.QueueBind(QueueErrands, ErrandKey, ExchangeEvents, false, nil)
}
