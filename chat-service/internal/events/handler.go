// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-10
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

package events

import (
	"context"
	"errors"
	"log/slog"

	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/chat"
)

// Disposition is what the consumer tells RabbitMQ about a delivery.
type Disposition int

const (
	// Ack: the event was applied and committed; RabbitMQ deletes it.
	Ack Disposition = iota
	// Reject: the event can never be applied. It goes straight to the
	// dead-letter queue.
	Reject
	// Retry: a temporary failure, such as the database being down. The event
	// is retried with growing backoff for as long as it takes: giving up
	// would lose the room, because order-service does not send events again.
	Retry
	// RetryLimited: an unexpected failure that might be caused by the event
	// itself. It is retried MaxAttempts times, then dead-lettered, so one bad
	// event cannot block the queue forever.
	RetryLimited
)

func (d Disposition) String() string {
	switch d {
	case Ack:
		return "ack"
	case Reject:
		return "reject"
	case Retry:
		return "retry"
	default:
		return "retry-limited"
	}
}

// Applier applies events to rooms; *chat.Lifecycle implements it.
type Applier interface {
	OnAccepted(ctx context.Context, event chat.AcceptedEvent) (chat.AcceptResult, error)
	OnWithdrawn(ctx context.Context, event chat.WithdrawnEvent) (chat.WithdrawResult, error)
	OnErrandEnded(ctx context.Context, event chat.ErrandEndedEvent) (chat.EndResult, error)
}

// Handler turns one delivery into a call on the Applier and decides its
// disposition. Logs carry the routing key, message ID and errand ID, never
// user IDs or the message body.
type Handler struct {
	applier Applier
	logger  *slog.Logger
}

func NewHandler(applier Applier, logger *slog.Logger) *Handler {
	if applier == nil || logger == nil {
		panic("events: applier and logger are required")
	}
	return &Handler{applier: applier, logger: logger}
}

// Handle applies one event. It acks only after the change is committed.
func (h *Handler) Handle(ctx context.Context, routingKey, messageID string, body []byte) Disposition {
	log := h.logger.With("routingKey", routingKey, "messageId", messageID)
	switch routingKey {
	case RoutingErrandAccepted:
		event, err := decodeAccepted(routingKey, body)
		if err != nil {
			log.WarnContext(ctx, "errand event rejected: malformed", "reason", err.Error())
			return Reject
		}
		log = log.With("errandId", event.ErrandID.String(), "errandVersion", event.ErrandVersion)
		result, err := h.applier.OnAccepted(ctx, event)
		if err != nil {
			return h.failed(ctx, log, err)
		}
		log.InfoContext(ctx, "errand.accepted applied",
			"roomId", result.Room.ID.String(), "roomOpened", result.Opened, "roomClosing", result.Closing)
		return Ack
	case RoutingErrandWithdrawn:
		event, err := decodeWithdrawn(routingKey, body)
		if err != nil {
			log.WarnContext(ctx, "errand event rejected: malformed", "reason", err.Error())
			return Reject
		}
		log = log.With("errandId", event.ErrandID.String(), "errandVersion", event.ErrandVersion)
		result, err := h.applier.OnWithdrawn(ctx, event)
		if err != nil {
			return h.failed(ctx, log, err)
		}
		log.InfoContext(ctx, "errand.withdrawn applied",
			"roomId", result.Room.ID.String(), "roomClosing", result.Closing)
		return Ack
	case RoutingErrandCompleted, RoutingErrandRefunded:
		reason := chat.EndCompleted
		if routingKey == RoutingErrandRefunded {
			reason = chat.EndRefunded
		}
		event, err := decodeErrandEnded(routingKey, body, reason)
		if err != nil {
			log.WarnContext(ctx, "errand event rejected: malformed", "reason", err.Error())
			return Reject
		}
		log = log.With("errandId", event.ErrandID.String(), "errandVersion", event.ErrandVersion)
		result, err := h.applier.OnErrandEnded(ctx, event)
		if err != nil {
			return h.failed(ctx, log, err)
		}
		log.InfoContext(ctx, routingKey+" applied", "roomsClosing", len(result.Closing))
		return Ack
	default:
		// Only bound keys arrive; anything else is a topology mistake.
		log.WarnContext(ctx, "errand event rejected: routing key not handled")
		return Reject
	}
}

func (h *Handler) failed(ctx context.Context, log *slog.Logger, err error) Disposition {
	switch {
	case errors.Is(err, chat.ErrInvalidEvent):
		log.WarnContext(ctx, "errand event rejected: cannot be applied", "reason", err.Error())
		return Reject
	case errors.Is(err, chat.ErrUnavailable):
		log.WarnContext(ctx, "errand event not applied: storage unavailable; will retry", "error", err.Error())
		return Retry
	default:
		log.ErrorContext(ctx, "errand event not applied: unexpected error; will retry a few times", "error", err.Error())
		return RetryLimited
	}
}
