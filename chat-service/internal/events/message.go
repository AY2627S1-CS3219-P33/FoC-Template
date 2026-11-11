// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-10
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

package events

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/chat"
)

// maxBodyBytes bounds an event body; real errand events are a few hundred
// bytes.
const maxBodyBytes = 64 * 1024

// errMalformed marks a message that breaks the contract in
// docs/asyncapi.yaml. It is dead-lettered, never retried.
var errMalformed = errors.New("malformed event")

// envelope holds the fields chat-service reads from the Envelope schema in
// docs/asyncapi.yaml. Unknown fields are ignored, so order-service can add
// fields without breaking chat-service.
type envelope struct {
	MessageID     string          `json:"messageId"`
	Type          string          `json:"type"`
	OccurredAt    time.Time       `json:"occurredAt"`
	ErrandID      string          `json:"errandId"`
	ErrandVersion int             `json:"errandVersion"`
	Data          json.RawMessage `json:"data"`
}

// errandEventData holds the fields chat-service reads from ErrandEventData.
type errandEventData struct {
	ErrandID      string `json:"errandId"`
	ErrandVersion int    `json:"errandVersion"`
	RequesterID   string `json:"requesterId"`
	CourierID     string `json:"courierId"`
}

// decodeAccepted checks an errand.accepted body and converts it. Every
// error wraps errMalformed. Error text names the problem, never a value.
func decodeAccepted(routingKey string, body []byte) (chat.AcceptedEvent, error) {
	env, data, err := decodeErrandEvent(routingKey, body)
	if err != nil {
		return chat.AcceptedEvent{}, err
	}
	if data.CourierID == "" {
		return chat.AcceptedEvent{}, fmt.Errorf("%w: data.courierId is required", errMalformed)
	}
	errandID, _ := uuid.Parse(env.ErrandID) // checked by decodeErrandEvent
	return chat.AcceptedEvent{
		ErrandID:      errandID,
		ErrandVersion: env.ErrandVersion,
		RequesterID:   data.RequesterID,
		CourierID:     data.CourierID,
		OccurredAt:    env.OccurredAt,
	}, nil
}

// decodeWithdrawn checks an errand.withdrawn body and converts it. data.courierId
// is the courier who withdrew or was removed (order-service sets it from the
// errand before the transition).
func decodeWithdrawn(routingKey string, body []byte) (chat.WithdrawnEvent, error) {
	env, data, err := decodeErrandEvent(routingKey, body)
	if err != nil {
		return chat.WithdrawnEvent{}, err
	}
	if data.CourierID == "" {
		return chat.WithdrawnEvent{}, fmt.Errorf("%w: data.courierId is required", errMalformed)
	}
	errandID, _ := uuid.Parse(env.ErrandID) // checked by decodeErrandEvent
	return chat.WithdrawnEvent{
		ErrandID:      errandID,
		ErrandVersion: env.ErrandVersion,
		RequesterID:   data.RequesterID,
		CourierID:     data.CourierID,
		OccurredAt:    env.OccurredAt,
	}, nil
}

// decodeErrandEnded checks an errand.completed or errand.refunded body. The
// courier is not needed: every room of the errand ends.
func decodeErrandEnded(routingKey string, body []byte, reason chat.EndReason) (chat.ErrandEndedEvent, error) {
	env, _, err := decodeErrandEvent(routingKey, body)
	if err != nil {
		return chat.ErrandEndedEvent{}, err
	}
	errandID, _ := uuid.Parse(env.ErrandID) // checked by decodeErrandEvent
	return chat.ErrandEndedEvent{
		ErrandID:      errandID,
		ErrandVersion: env.ErrandVersion,
		Reason:        reason,
		OccurredAt:    env.OccurredAt,
	}, nil
}

// decodeErrandEvent checks the envelope and the data fields every errand
// event has.
func decodeErrandEvent(routingKey string, body []byte) (envelope, errandEventData, error) {
	fail := func(problem string) (envelope, errandEventData, error) {
		return envelope{}, errandEventData{}, fmt.Errorf("%w: %s", errMalformed, problem)
	}
	if len(body) > maxBodyBytes {
		return fail("body is too large")
	}
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return fail("body is not a JSON envelope")
	}
	switch {
	case env.MessageID == "":
		return fail("messageId is required")
	case env.Type != routingKey:
		return fail("type must equal the routing key")
	case env.OccurredAt.IsZero():
		return fail("occurredAt is required")
	case env.ErrandVersion < 1:
		return fail("errandVersion must be 1 or more")
	}
	if _, err := uuid.Parse(env.ErrandID); err != nil {
		return fail("errandId must be a UUID")
	}
	if len(bytes.TrimSpace(env.Data)) == 0 || bytes.TrimSpace(env.Data)[0] != '{' {
		return fail("data must be an object")
	}
	var data errandEventData
	if err := json.Unmarshal(env.Data, &data); err != nil {
		return fail("data does not match ErrandEventData")
	}
	switch {
	case data.ErrandID != env.ErrandID || data.ErrandVersion != env.ErrandVersion:
		return fail("data.errandId and data.errandVersion must match the envelope")
	case data.RequesterID == "":
		return fail("data.requesterId is required")
	}
	return env, data, nil
}
