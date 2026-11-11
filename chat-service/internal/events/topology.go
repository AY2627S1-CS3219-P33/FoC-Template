// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-10
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

// Package events consumes the errand events order-service publishes on
// RabbitMQ and applies them to chat rooms through chat.Lifecycle. It owns
// chat-service's queue and dead-letter queue; the exchange belongs to
// order-service and is declared here with identical arguments, so either
// service can start first.
package events

import amqp "github.com/rabbitmq/amqp091-go"

const (
	// Exchange is order-service's durable topic exchange for events.
	Exchange = "foc.events"
	// Queue holds the errand events chat-service has not processed yet. It
	// is durable, so events published while chat-service is down wait here.
	Queue = "chat-service.errand-events"
	// DeadLetterQueue receives events that can never be applied (broken
	// JSON, missing fields), that failed unexpectedly MaxAttempts times, or
	// that were delivered DeliveryLimit times without an answer. Nothing
	// reads it automatically; inspect it in the RabbitMQ management UI.
	DeadLetterQueue = "chat-service.errand-events.dlq"
	// DeliveryLimit is how many times RabbitMQ hands one message to a consumer
	// that then disappears without answering (a crash or a lost connection)
	// before dead-lettering it. RabbitMQ 4 does not count explicit requeues
	// towards it, so the consumer limits those itself (MaxAttempts).
	DeliveryLimit = 5
)

// Routing keys chat-service binds to: an acceptance opens a room, and a
// withdrawal (that courier's room), completion or refund (every room of the
// errand) starts its closing. Delivery and disputes do not end a room.
const (
	RoutingErrandAccepted  = "errand.accepted"
	RoutingErrandWithdrawn = "errand.withdrawn"
	RoutingErrandCompleted = "errand.completed"
	RoutingErrandRefunded  = "errand.refunded"
)

// Bindings lists every routing key Queue receives. Binding is additive, so a
// queue declared by an older build gains the new keys on the next connect.
func Bindings() []string {
	return []string{RoutingErrandAccepted, RoutingErrandWithdrawn, RoutingErrandCompleted, RoutingErrandRefunded}
}

// Declare creates the exchange, both queues and the bindings if they do not
// exist. It is safe to call on every connect; RabbitMQ refuses it only if
// one already exists with different arguments.
func Declare(ch *amqp.Channel) error {
	// Same arguments as order-service's messaging.DeclareTopology.
	if err := ch.ExchangeDeclare(Exchange, "topic", true, false, false, false, nil); err != nil {
		return err
	}
	if _, err := ch.QueueDeclare(DeadLetterQueue, true, false, false, false, amqp.Table{
		"x-queue-type": "quorum",
	}); err != nil {
		return err
	}
	if _, err := ch.QueueDeclare(Queue, true, false, false, false, amqp.Table{
		"x-queue-type":              "quorum",
		"x-delivery-limit":          int32(DeliveryLimit),
		"x-dead-letter-exchange":    "",
		"x-dead-letter-routing-key": DeadLetterQueue,
	}); err != nil {
		return err
	}
	for _, key := range Bindings() {
		if err := ch.QueueBind(Queue, key, Exchange, false, nil); err != nil {
			return err
		}
	}
	return nil
}
