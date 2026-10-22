// Package creditmsg is the message layer of credit-service: it turns the
// bytes of a RabbitMQ message into a call on the wallet use cases, and a
// use-case result into the bytes of a result message. It knows the shapes in
// docs/asyncapi.yaml and nothing about the broker, so it is tested without
// one. Users in these messages are Auth0 subjects and orders are errands.
package creditmsg

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Routing keys of the credit commands (foc.commands) and results (foc.events).
const (
	CommandReserve  = "credit.reserve"
	CommandAdjust   = "credit.adjust"
	CommandRelease  = "credit.release"
	CommandTransfer = "credit.transfer"
)

// ErrUndecodable means a message can never be processed, however often it is
// tried: unknown routing key, bad JSON, or data that breaks the contract.
var ErrUndecodable = errors.New("undecodable message")

// Envelope is the body of every message in both directions.
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

// decodeEnvelope parses a message body and checks the rules every command
// shares: the type equals the routing key, and the identifying fields exist.
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

// decodeData parses the data of a command into dst and checks that its
// errandId and errandVersion repeat the envelope's.
func decodeData(env Envelope, dst interface {
	ids() (errandID string, errandVersion int64)
}) error {
	decoder := json.NewDecoder(bytes.NewReader(env.Data))
	if err := decoder.Decode(dst); err != nil {
		return undecodable("data does not match %s", env.Type)
	}
	errandID, version := dst.ids()
	if !strings.EqualFold(errandID, env.ErrandID) || version != env.ErrandVersion {
		return undecodable("data errandId and errandVersion differ from the envelope")
	}
	return nil
}

// Data of the commands, as in ReserveCreditData and the schemas next to it.

type reserveData struct {
	ErrandID      string `json:"errandId"`
	ErrandVersion int64  `json:"errandVersion"`
	RequesterID   string `json:"requesterId"`
	Amount        int64  `json:"amount"`
}

type adjustData struct {
	ErrandID      string `json:"errandId"`
	ErrandVersion int64  `json:"errandVersion"`
	RequesterID   string `json:"requesterId"`
	NewAmount     int64  `json:"newAmount"`
	Seq           int64  `json:"seq"`
}

type releaseData struct {
	ErrandID      string `json:"errandId"`
	ErrandVersion int64  `json:"errandVersion"`
	RequesterID   string `json:"requesterId"`
}

type transferData struct {
	ErrandID      string `json:"errandId"`
	ErrandVersion int64  `json:"errandVersion"`
	RequesterID   string `json:"requesterId"`
	CourierID     string `json:"courierId"`
	Amount        int64  `json:"amount"`
}

func (d *reserveData) ids() (string, int64)  { return d.ErrandID, d.ErrandVersion }
func (d *adjustData) ids() (string, int64)   { return d.ErrandID, d.ErrandVersion }
func (d *releaseData) ids() (string, int64)  { return d.ErrandID, d.ErrandVersion }
func (d *transferData) ids() (string, int64) { return d.ErrandID, d.ErrandVersion }
