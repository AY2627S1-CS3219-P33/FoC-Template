// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-09
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

// Package facts is admin-service's read model: the events order-service
// published, and the per-errand state and times distilled from them. The
// metrics are computed from these two tables at query time.
package facts

import (
	"errors"
	"fmt"
	"regexp"
	"time"
)

// Status is an errand status, as order-service names them.
type Status string

const (
	StatusPendingReservation Status = "PENDING_RESERVATION"
	StatusOpen               Status = "OPEN"
	StatusAssigned           Status = "ASSIGNED"
	StatusPickedUp           Status = "PICKED_UP"
	StatusDelivered          Status = "DELIVERED"
	StatusReviewRequired     Status = "REVIEW_REQUIRED"
	StatusRejected           Status = "REJECTED"
	StatusCancelled          Status = "CANCELLED"
	StatusExpired            Status = "EXPIRED"
	StatusCompleted          Status = "COMPLETED"
	StatusRefunded           Status = "REFUNDED"
)

var statuses = map[Status]bool{
	StatusPendingReservation: true, StatusOpen: true, StatusAssigned: true, StatusPickedUp: true,
	StatusDelivered: true, StatusReviewRequired: true, StatusRejected: true, StatusCancelled: true,
	StatusExpired: true, StatusCompleted: true, StatusRefunded: true,
}

// Valid reports whether s is one of the eleven statuses.
func (s Status) Valid() bool { return statuses[s] }

// Terminal reports whether an errand in s has finished: COMPLETED, REFUNDED,
// REJECTED, CANCELLED or EXPIRED.
func (s Status) Terminal() bool {
	switch s {
	case StatusCompleted, StatusRefunded, StatusRejected, StatusCancelled, StatusExpired:
		return true
	}
	return false
}

// TerminalStatuses lists the five terminal statuses, in the order the
// dashboard shows them.
func TerminalStatuses() []Status {
	return []Status{StatusCompleted, StatusCancelled, StatusExpired, StatusRejected, StatusRefunded}
}

// Event types: the routing keys of order-service's transition events
// (docs/asyncapi.yaml). Rejection events and reminders share the errand.#
// binding but are not transitions, and are ignored.
const (
	TypePlaced         = "errand.placed"
	TypeOpened         = "errand.opened"
	TypeRejected       = "errand.rejected"
	TypeEdited         = "errand.edited"
	TypeEditRequested  = "errand.edit_requested"
	TypeEditRejected   = "errand.edit_rejected"
	TypeAccepted       = "errand.accepted"
	TypeCancelled      = "errand.cancelled"
	TypeWithdrawn      = "errand.withdrawn"
	TypeExpired        = "errand.expired"
	TypePickedUp       = "errand.picked_up"
	TypeDelivered      = "errand.delivered"
	TypeReviewRequired = "errand.review_required"
	TypeCompleted      = "errand.completed"
	TypeRefunded       = "errand.refunded"
)

var transitionTypes = map[string]bool{
	TypePlaced: true, TypeOpened: true, TypeRejected: true, TypeEdited: true, TypeEditRequested: true,
	TypeEditRejected: true, TypeAccepted: true, TypeCancelled: true, TypeWithdrawn: true, TypeExpired: true,
	TypePickedUp: true, TypeDelivered: true, TypeReviewRequired: true, TypeCompleted: true, TypeRefunded: true,
}

// IsTransitionType reports whether a routing key is one of the 15 transition
// events the read model records.
func IsTransitionType(eventType string) bool { return transitionTypes[eventType] }

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Event is one transition event, from the live stream or the export.
type Event struct {
	// MessageID is the event's messageId, "{errandId}:{event}:{version}".
	MessageID string
	// Type is the routing key, one of the Type constants.
	Type     string
	ErrandID string
	// Version is the errand's version after the change.
	Version int
	// From is empty for the creation transition.
	From Status
	To   Status
	// CourierID is empty when the event names no courier.
	CourierID  string
	Reward     int
	OccurredAt time.Time
}

// Validate reports why the event cannot be recorded. An event that fails can
// never succeed on redelivery.
func (e Event) Validate() error {
	switch {
	case e.MessageID == "":
		return errors.New("messageId is required")
	case !IsTransitionType(e.Type):
		return fmt.Errorf("%q is not a transition event", e.Type)
	case !uuidPattern.MatchString(e.ErrandID):
		return errors.New("errandId must be a lower-case UUID")
	case e.Version < 1:
		return errors.New("errandVersion must be at least 1")
	case !e.To.Valid():
		return errors.New("to is not an errand status")
	case e.From != "" && !e.From.Valid():
		return errors.New("from is not an errand status")
	case e.Reward < 0:
		return errors.New("reward must not be negative")
	case e.OccurredAt.IsZero():
		return errors.New("occurredAt is required")
	case e.terminalType() && !e.To.Terminal():
		return fmt.Errorf("%s must end in a terminal status", e.Type)
	}
	return nil
}

// terminalType reports whether the event is one of the five that finish an
// errand.
func (e Event) terminalType() bool {
	switch e.Type {
	case TypeCompleted, TypeRefunded, TypeRejected, TypeCancelled, TypeExpired:
		return true
	}
	return false
}

// Fact is one errand's row in errand_facts, without the enrichment columns.
type Fact struct {
	ErrandID string
	Status   Status
	Version  int
	// LastEventAt is when the event that set Status and Version occurred.
	LastEventAt time.Time
	// CourierID is the courier the latest event named; empty if none.
	CourierID       string
	Reward          int
	OpenedAt        *time.Time
	FirstAcceptedAt *time.Time
	TerminalStatus  Status
	TerminalAt      *time.Time
	EnrichmentDue   bool
}

// NewFact is the row an errand's first event creates.
func NewFact(e Event) Fact {
	return Fact{}.merge(e, true)
}

// Merge returns the row after e. Events arrive at least once and in any
// order, so every rule is safe to apply twice and in either order:
//
//   - Status, version, courier and reward follow the newest event: a higher
//     version wins; at an equal version (the withdrawn/expired pair) the
//     later occurredAt wins, and at an equal time a terminal event wins, so
//     a withdrawn that arrives after its expired cannot reopen the errand.
//   - openedAt is the errand.opened time, the earliest if seen twice.
//   - firstAcceptedAt is the earliest accept, so a later withdraw and
//     re-accept does not change it, and neither does an accept arriving late.
//   - The terminal status and time are set by the one terminal event.
//   - An accepted or terminal event makes the errand due for enrichment.
func (f Fact) Merge(e Event) Fact { return f.merge(e, false) }

func (f Fact) merge(e Event, first bool) Fact {
	if first {
		f = Fact{ErrandID: e.ErrandID}
	}
	if first || newer(f, e) {
		f.Status, f.Version, f.LastEventAt = e.To, e.Version, e.OccurredAt
		f.CourierID, f.Reward = e.CourierID, e.Reward
	}
	switch e.Type {
	case TypeOpened:
		f.OpenedAt = earlier(f.OpenedAt, e.OccurredAt)
	case TypeAccepted:
		f.FirstAcceptedAt = earlier(f.FirstAcceptedAt, e.OccurredAt)
		f.EnrichmentDue = true
	}
	if e.terminalType() {
		if f.TerminalAt == nil {
			at := e.OccurredAt
			f.TerminalStatus, f.TerminalAt = e.To, &at
		}
		f.EnrichmentDue = true
	}
	return f
}

// newer reports whether e is later than the event that set f's state.
func newer(f Fact, e Event) bool {
	switch {
	case e.Version != f.Version:
		return e.Version > f.Version
	case !e.OccurredAt.Equal(f.LastEventAt):
		return e.OccurredAt.After(f.LastEventAt)
	default:
		return e.To.Terminal() && !f.Status.Terminal()
	}
}

func earlier(current *time.Time, candidate time.Time) *time.Time {
	if current == nil || candidate.Before(*current) {
		return &candidate
	}
	return current
}
