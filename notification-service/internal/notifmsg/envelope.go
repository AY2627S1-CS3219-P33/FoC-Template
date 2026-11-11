// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: Implementation of notification-service notification event messaging.
// Author review: I validated correctness, edited for style, and added boundary checks.

// Package notifmsg is the message layer of notification-service: it turns the
// bytes of a RabbitMQ errand event into user-facing notifications and stores
// them. It knows the shapes in docs/asyncapi.yaml and nothing about the
// broker, so it is tested without one. Users in these messages are Auth0
// subjects and orders are errands.
package notifmsg

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ErrUndecodable means a message can never be processed, however often it is
// tried: unknown routing key, bad JSON, or data that breaks the contract.
var ErrUndecodable = errors.New("undecodable message")

// Envelope is the body shared by every event on foc.events.
type Envelope struct {
	MessageID     string          `json:"messageId"`
	CorrelationID string          `json:"correlationId,omitempty"`
	Type          string          `json:"type"`
	OccurredAt    time.Time       `json:"occurredAt"`
	ErrandID      string          `json:"errandId"`
	ErrandVersion int64           `json:"errandVersion"`
	Data          json.RawMessage `json:"data"`
}

func undecodable(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrUndecodable, fmt.Sprintf(format, args...))
}

// decodeEnvelope parses a message body and checks the rules every event
// shares: the type equals the routing key and the identifying fields exist.
func decodeEnvelope(routingKey string, body []byte) (Envelope, error) {
	var env Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return Envelope{}, undecodable("body is not a valid envelope")
	}
	switch {
	case env.MessageID == "":
		return Envelope{}, undecodable("messageId is missing")
	case env.Type != routingKey:
		return Envelope{}, undecodable("type %q does not match routing key %q", env.Type, routingKey)
	case env.ErrandID == "":
		return Envelope{}, undecodable("errandId is missing")
	case env.ErrandVersion < 1:
		return Envelope{}, undecodable("errandVersion must be at least 1")
	}
	return env, nil
}

func decodeData(env Envelope, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(env.Data))
	if err := decoder.Decode(dst); err != nil {
		return undecodable("data does not match %s", env.Type)
	}
	return nil
}

// errandEventData is the payload of the lifecycle events (ErrandEventData in
// docs/asyncapi.yaml). Only the fields that choose a recipient or describe the
// change are read; contact names and mobile numbers are never published and so
// never appear here.
type errandEventData struct {
	Event        string `json:"event"`
	From         string `json:"from"`
	To           string `json:"to"`
	ActorType    string `json:"actorType"`
	ActorID      string `json:"actorId"`
	Reason       string `json:"reason"`
	ReviewReason string `json:"reviewReason"`
	RequesterID  string `json:"requesterId"`
	CourierID    string `json:"courierId"`
	Reward       int64  `json:"reward"`
}

// rejectionData is the payload of accept/cancel/pickup rejections
// (RejectionData): the actor's action could not be applied.
type rejectionData struct {
	Status    string `json:"status"`
	Action    string `json:"action"`
	Code      string `json:"code"`
	ActorType string `json:"actorType"`
	ActorID   string `json:"actorId"`
}

// reminderData is the payload of the deadline reminders (ReminderData).
type reminderData struct {
	Status      string    `json:"status"`
	RequesterID string    `json:"requesterId"`
	CourierID   string    `json:"courierId"`
	Deadline    time.Time `json:"deadline"`
}
