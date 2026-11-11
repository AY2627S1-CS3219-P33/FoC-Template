// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-09
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

// Package events consumes order-service's errand events into the read model.
//
// The queue admin-service.events is bound to errand.# only. Delivery is
// at-least-once and may reorder, so the handler deduplicates by messageId,
// merges by errandVersion (internal/facts), and acknowledges only after the
// database commit; a crash in between redelivers a message that then changes
// nothing. A message that can never be processed is rejected without requeue,
// so it dead-letters instead of blocking the queue.
package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/facts"
)

// Disposition is how a delivery is settled.
type Disposition int

const (
	// Ack: the event is recorded, was already recorded, or is not one the
	// read model keeps.
	Ack Disposition = iota
	// Reject dead-letters the message: it can never be processed.
	Reject
	// Requeue puts the message back after a failure that may pass, such as a
	// database error.
	Requeue
)

func (d Disposition) String() string {
	switch d {
	case Ack:
		return "ack"
	case Reject:
		return "reject"
	default:
		return "requeue"
	}
}

// Recorder stores one event; *facts.Store implements it. It reports whether
// the event was new.
type Recorder interface {
	Apply(ctx context.Context, e facts.Event) (bool, error)
}

var _ Recorder = (*facts.Store)(nil)

// Handler decodes one delivery and records it.
type Handler struct {
	recorder Recorder
	// enrichmentDue is called after an event that makes an errand owe
	// enrichment was recorded; it must not block. May be nil.
	enrichmentDue func()
	logger        *slog.Logger
}

func NewHandler(recorder Recorder, enrichmentDue func(), logger *slog.Logger) *Handler {
	if recorder == nil || logger == nil {
		panic("events: handler dependencies are required")
	}
	return &Handler{recorder: recorder, enrichmentDue: enrichmentDue, logger: logger}
}

// envelope is order-service's errand.Envelope, with the fields the read
// model uses. Unknown fields are ignored so the contract can grow.
type envelope struct {
	MessageID     string    `json:"messageId"`
	Type          string    `json:"type"`
	OccurredAt    time.Time `json:"occurredAt"`
	ErrandID      string    `json:"errandId"`
	ErrandVersion int       `json:"errandVersion"`
	Data          struct {
		From      facts.Status `json:"from"`
		To        facts.Status `json:"to"`
		CourierID string       `json:"courierId"`
		Reward    int          `json:"reward"`
	} `json:"data"`
}

// Decode turns a message body into an event. It fails for a body that is not
// a transition event envelope, which no redelivery can fix.
func Decode(routingKey string, body []byte) (facts.Event, error) {
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return facts.Event{}, errors.New("body is not a JSON event envelope")
	}
	if env.Type != routingKey {
		return facts.Event{}, fmt.Errorf("type %q does not match routing key %q", env.Type, routingKey)
	}
	e := facts.Event{
		MessageID:  env.MessageID,
		Type:       env.Type,
		ErrandID:   env.ErrandID,
		Version:    env.ErrandVersion,
		From:       env.Data.From,
		To:         env.Data.To,
		CourierID:  env.Data.CourierID,
		Reward:     env.Data.Reward,
		OccurredAt: env.OccurredAt,
	}
	if err := e.Validate(); err != nil {
		return facts.Event{}, err
	}
	return e, nil
}

// Handle records one delivery and says how to settle it.
func (h *Handler) Handle(ctx context.Context, routingKey string, body []byte) Disposition {
	// errand.# also carries rejection events and reminders. They are valid
	// messages the read model has no use for.
	if !facts.IsTransitionType(routingKey) {
		if strings.HasPrefix(routingKey, "errand.") {
			h.logger.DebugContext(ctx, "ignoring an errand event the read model does not keep", slog.String("routing_key", routingKey))
		} else {
			h.logger.WarnContext(ctx, "ignoring a message outside errand.#", slog.String("routing_key", routingKey))
		}
		return Ack
	}
	event, err := Decode(routingKey, body)
	if err != nil {
		h.logger.WarnContext(ctx, "rejecting an event that cannot be processed",
			slog.String("routing_key", routingKey), slog.String("reason", err.Error()))
		return Reject
	}
	applied, err := h.recorder.Apply(ctx, event)
	switch {
	case errors.Is(err, facts.ErrInvalid):
		h.logger.WarnContext(ctx, "rejecting an event the store refused",
			slog.String("message_id", event.MessageID), slog.String("reason", err.Error()))
		return Reject
	case err != nil:
		h.logger.ErrorContext(ctx, "could not record an event; it will be retried",
			slog.String("message_id", event.MessageID), slog.Any("error", err))
		return Requeue
	}
	if !applied {
		h.logger.DebugContext(ctx, "event already recorded", slog.String("message_id", event.MessageID))
		return Ack
	}
	if h.enrichmentDue != nil && (event.Type == facts.TypeAccepted || event.To.Terminal()) {
		h.enrichmentDue()
	}
	return Ack
}
