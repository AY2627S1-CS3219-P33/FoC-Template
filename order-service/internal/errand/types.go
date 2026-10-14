// Package errand holds the errand domain: states, events, the Diagram 1
// transition table and the outbox rows each transition emits. Everything in
// this package's top-level files is pure: no I/O and no wall clock.
package errand

import (
	"encoding/json"
	"time"
)

// Status is an errand state from Diagram 1 in order-requirements.md. The
// values match the errands.status CHECK constraint.
type Status string

const (
	// StatusNone is the "(start)" pseudo-state before an errand exists. Only
	// Submit leaves it. It is never stored.
	StatusNone Status = ""

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

// Statuses lists the 11 stored states in lifecycle order.
func Statuses() []Status {
	return []Status{
		StatusPendingReservation,
		StatusOpen,
		StatusAssigned,
		StatusPickedUp,
		StatusDelivered,
		StatusReviewRequired,
		StatusRejected,
		StatusCancelled,
		StatusExpired,
		StatusCompleted,
		StatusRefunded,
	}
}

// Terminal reports whether no transition leaves s. Every other stored state
// is "active" (F1.2.2, F1.2.3, F3.11).
func (s Status) Terminal() bool {
	switch s {
	case StatusRejected, StatusCancelled, StatusExpired, StatusCompleted, StatusRefunded:
		return true
	default:
		return false
	}
}

// Active reports whether s is a stored, non-terminal state.
func (s Status) Active() bool {
	return s != StatusNone && !s.Terminal() && s.Valid()
}

// Valid reports whether s is one of the 11 stored states.
func (s Status) Valid() bool {
	for _, known := range Statuses() {
		if s == known {
			return true
		}
	}
	return false
}

// Event is something that may move an errand between states. The value is
// what errand_transitions.event records.
type Event string

const (
	// Creation.
	EventSubmit               Event = "submit"
	EventReservationConfirmed Event = "reservation_confirmed"
	EventReservationRefused   Event = "reservation_refused"
	// EventReservationFailed compensates a reservation with no result after
	// the reconciler's timeout.
	EventReservationFailed Event = "reservation_failed"

	// Requester.
	EventEdit    Event = "edit"
	EventCancel  Event = "cancel"
	EventConfirm Event = "confirm"
	EventDispute Event = "dispute"

	// Reward-edit outcomes, from credit results or the edit reconciler.
	EventEditConfirmed Event = "edit_confirmed"
	EventEditRefused   Event = "edit_refused"

	// Courier.
	EventAccept      Event = "accept"
	EventWithdraw    Event = "withdraw"
	EventPickUp      Event = "pick_up"
	EventDeliver     Event = "deliver"
	EventReportIssue Event = "report_issue"

	// Deadlines.
	EventAcceptanceDeadlinePassed Event = "acceptance_deadline_passed"
	EventPickupDeadlinePassed     Event = "pickup_deadline_passed"
	EventDeliveryDeadlinePassed   Event = "delivery_deadline_passed"
	EventVerificationLapsed       Event = "verification_lapsed"

	// Admin review resolution (F6.1.2, F6.1.3).
	EventAdminComplete Event = "admin_complete"
	EventAdminRefund   Event = "admin_refund"
)

// Events lists every event.
func Events() []Event {
	return []Event{
		EventSubmit,
		EventReservationConfirmed,
		EventReservationRefused,
		EventReservationFailed,
		EventEdit,
		EventCancel,
		EventConfirm,
		EventDispute,
		EventEditConfirmed,
		EventEditRefused,
		EventAccept,
		EventWithdraw,
		EventPickUp,
		EventDeliver,
		EventReportIssue,
		EventAcceptanceDeadlinePassed,
		EventPickupDeadlinePassed,
		EventDeliveryDeadlinePassed,
		EventVerificationLapsed,
		EventAdminComplete,
		EventAdminRefund,
	}
}

// ActorType is who caused a transition, as errand_transitions.actor_type
// records it. REQUESTER and COURIER are per-errand relationships, not
// account roles (F1.5).
type ActorType string

const (
	ActorRequester ActorType = "REQUESTER"
	ActorCourier   ActorType = "COURIER"
	ActorAdmin     ActorType = "ADMIN"
	ActorSystem    ActorType = "SYSTEM"
)

// Actor is the caller of Next. Type is the role the caller acts in; Next
// checks it against the errand (NFR3.3.2). ID is the identity provider
// subject, empty for SYSTEM.
type Actor struct {
	Type ActorType
	ID   string
}

// SystemActor is the actor for scheduler, consumer and reconciler events.
func SystemActor() Actor { return Actor{Type: ActorSystem} }

// ReviewReason is why an errand is in REVIEW_REQUIRED (F6.1).
type ReviewReason string

const (
	ReviewCourierIssue     ReviewReason = "COURIER_ISSUE"
	ReviewRequesterDispute ReviewReason = "REQUESTER_DISPUTE"
	ReviewDeliveryOverdue  ReviewReason = "DELIVERY_OVERDUE"
)

// Rejection reasons recorded on REJECTED errands and refused edits.
const (
	ReasonInsufficientCredits = "INSUFFICIENT_CREDITS"
	ReasonReservationFailed   = "RESERVATION_FAILED"
	ReasonEditFailed          = "EDIT_FAILED"
)

// Details are the requester-editable errand fields (F3.1.1, F3.4). An edit
// always carries the complete set, so a pending edit can be applied as is.
type Details struct {
	SupplierID        string `json:"supplierId"`
	SupplierVersionID string `json:"supplierVersionId"`
	// DeliveryBuildingSnapshot is the supplier-service building record at
	// the time of the request, stored as is (F3.1.1).
	DeliveryBuildingID       string          `json:"deliveryBuildingId"`
	DeliveryBuildingSnapshot json.RawMessage `json:"deliveryBuildingSnapshot"`
	DeliveryDetail           string          `json:"deliveryDetail"`
	// Items is a JSON array with at least one element. The element shape is
	// defined by the OpenAPI contract, not by the domain.
	Items              json.RawMessage `json:"items"`
	Notes              string          `json:"notes"`
	Reward             int             `json:"reward"`
	AcceptanceDeadline time.Time       `json:"acceptanceDeadline"`
}

// PendingEdit is a reward edit waiting for credit-service (F4.4). It maps to
// the pending_edit, pending_edit_key and pending_edit_at columns.
type PendingEdit struct {
	Details Details
	// Key is the idempotency key of the credit.adjust row the edit sent.
	// Credit results are matched against it.
	Key string
	At  time.Time
}

// Errand is one row of the errands table. Empty strings and nil pointers
// stand for NULL columns.
type Errand struct {
	ID          string
	Status      Status
	Version     int
	RequesterID string
	CourierID   string
	Details     Details

	PickupDeadline       *time.Time
	DeliveryDeadline     *time.Time
	DeliveredAt          *time.Time
	VerificationDeadline *time.Time
	ReviewReason         ReviewReason

	CreateIdempotencyKey string
	PendingEdit          *PendingEdit

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Update is a change to one nullable errand column. The zero value leaves
// the column unchanged; Set with a nil Value sets it to NULL.
type Update[T any] struct {
	Set   bool
	Value *T
}

func setTo[T any](v T) Update[T] { return Update[T]{Set: true, Value: &v} }

func setNull[T any]() Update[T] { return Update[T]{Set: true} }

func (u Update[T]) applyTo(current *T) *T {
	if !u.Set {
		return current
	}
	return u.Value
}

// applyValue is applyTo for columns whose Go zero value stands for NULL.
func (u Update[T]) applyValue(current T) T {
	if !u.Set {
		return current
	}
	if u.Value == nil {
		var zero T
		return zero
	}
	return *u.Value
}

// Changes are the column updates a transition makes, besides status and
// version.
type Changes struct {
	CourierID Update[string]
	// Details, when non-nil, replaces every editable field (an applied edit).
	Details              *Details
	PickupDeadline       Update[time.Time]
	DeliveryDeadline     Update[time.Time]
	DeliveredAt          Update[time.Time]
	VerificationDeadline Update[time.Time]
	ReviewReason         Update[ReviewReason]
	PendingEdit          Update[PendingEdit]
}

// Transition is the result of Next: everything Store.Apply writes in one
// database transaction.
type Transition struct {
	ErrandID string
	From     Status
	To       Status
	Event    Event
	// Actor is the caller as recorded in errand_transitions.
	Actor  Actor
	Reason string

	// ExpectedVersion and ExpectedPendingEditKey are the compare-and-swap
	// condition: the store applies the transition only if the errand still
	// has this version and this pending_edit_key ("" meaning NULL). The key
	// is part of the condition because a reward-edit request does not change
	// the version. Both are zero for Submit.
	ExpectedVersion        int
	ExpectedPendingEditKey string

	// NewVersion is ExpectedVersion+1, except for a reward-edit request,
	// which keeps the version so couriers can still accept the current
	// details (F3.4.2).
	NewVersion int
	Changes    Changes
	Outbox     []OutboxRow
	At         time.Time
}

// ApplyTo returns e as it is after the transition. Stores and fakes use it to
// compute the new row.
func (t Transition) ApplyTo(e Errand) Errand {
	e.Status = t.To
	e.Version = t.NewVersion
	e.UpdatedAt = t.At
	if t.From == StatusNone {
		e.CreatedAt = t.At
	}

	c := t.Changes
	if c.Details != nil {
		e.Details = *c.Details
	}
	e.CourierID = c.CourierID.applyValue(e.CourierID)
	e.PickupDeadline = c.PickupDeadline.applyTo(e.PickupDeadline)
	e.DeliveryDeadline = c.DeliveryDeadline.applyTo(e.DeliveryDeadline)
	e.DeliveredAt = c.DeliveredAt.applyTo(e.DeliveredAt)
	e.VerificationDeadline = c.VerificationDeadline.applyTo(e.VerificationDeadline)
	e.ReviewReason = c.ReviewReason.applyValue(e.ReviewReason)
	e.PendingEdit = c.PendingEdit.applyTo(e.PendingEdit)
	return e
}

// EventInput carries what Next needs beyond the errand, the actor and the
// time. Anything that needs history or settings is looked up by the caller,
// so Next never queries anything.
type EventInput struct {
	// Edit is the complete requested edit (EventEdit).
	Edit *Details
	// Reason is the free-text reason: required for ReportIssue, Dispute,
	// AdminComplete and AdminRefund (F3.6.2, F6.1.4); optional elsewhere.
	Reason string

	// PreviouslyWithdrew is true when the accepting courier withdrew from
	// this errand before, voluntarily or forced (F3.6.3).
	PreviouslyWithdrew bool
	// Restricted is true when the accepting courier is under a temporary
	// acceptance restriction (F3.6.4).
	Restricted bool

	// Deadlines are the current settings, needed by Accept and Deliver.
	Deadlines DeadlineSettings

	// EditKey is the credit.adjust key a credit result answers
	// (EditConfirmed, EditRefused). It must match the pending edit.
	EditKey string
	// EditTimedOut is true when the edit reconciler refuses an edit that got
	// no credit result. The adjustment may have landed, so the refusal also
	// sends a reverting credit.adjust.
	EditTimedOut bool
}
