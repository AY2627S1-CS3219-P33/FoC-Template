package errand

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
)

// Relationship is who may cause a transition, relative to the errand.
type Relationship string

const (
	RelRequester       Relationship = "requester"
	RelAssignedCourier Relationship = "assigned courier"
	// RelEligibleCourier is any courier who passes F3.3.6.
	RelEligibleCourier Relationship = "eligible courier"
	RelAdmin           Relationship = "admin"
	RelSystem          Relationship = "system"
)

// Guard is a deadline condition a rule needs at the time of the event.
type Guard string

const (
	GuardBeforeAcceptanceDeadline  Guard = "before acceptance deadline"
	GuardAfterAcceptanceDeadline   Guard = "after acceptance deadline"
	GuardAfterPickupDeadline       Guard = "after pickup deadline"
	GuardAfterDeliveryDeadline     Guard = "after delivery deadline"
	GuardAfterVerificationDeadline Guard = "after verification deadline"
)

// CreditEffect is the credit operation a rule requests (Diagram 1, "Credit
// effect"). Reward-edit adjustments are not a rule-level effect: they depend
// on the edit and on whether an edit is pending.
type CreditEffect string

const (
	CreditNone     CreditEffect = ""
	CreditReserve  CreditEffect = "reserve"
	CreditRelease  CreditEffect = "release"
	CreditTransfer CreditEffect = "transfer"
)

// Rule is one row of the Diagram 1 transition table. Rules with the same
// From and Event are alternatives chosen by their guards.
type Rule struct {
	From   Status
	Event  Event
	To     Status
	Actor  Relationship
	Guards []Guard
	Credit CreditEffect
	// Emits are the lifecycle events, in order.
	Emits []EventName
	// Review is the review reason set on entering REVIEW_REQUIRED.
	Review ReviewReason
	// NeedsReason means EventInput.Reason must not be blank.
	NeedsReason bool
}

var transitionTable = []Rule{
	{From: StatusNone, Event: EventSubmit, To: StatusPendingReservation, Actor: RelRequester,
		Credit: CreditReserve, Emits: []EventName{ErrandPlaced}},

	{From: StatusPendingReservation, Event: EventReservationConfirmed, To: StatusOpen, Actor: RelSystem,
		Emits: []EventName{ErrandOpened}},
	{From: StatusPendingReservation, Event: EventReservationRefused, To: StatusRejected, Actor: RelSystem,
		Emits: []EventName{ErrandRejected}},
	// Compensation: the reserve may have landed, so release it (NFR7.5.1).
	{From: StatusPendingReservation, Event: EventReservationFailed, To: StatusRejected, Actor: RelSystem,
		Credit: CreditRelease, Emits: []EventName{ErrandRejected}},

	// Emits is for an edit that leaves the reward alone; a reward change
	// emits errand.edit_requested instead (see outboxRows).
	{From: StatusOpen, Event: EventEdit, To: StatusOpen, Actor: RelRequester,
		Emits: []EventName{ErrandEdited}},
	{From: StatusOpen, Event: EventEditConfirmed, To: StatusOpen, Actor: RelSystem,
		Emits: []EventName{ErrandEdited}},
	{From: StatusOpen, Event: EventEditRefused, To: StatusOpen, Actor: RelSystem,
		Emits: []EventName{ErrandEditRejected}},
	{From: StatusOpen, Event: EventAccept, To: StatusAssigned, Actor: RelEligibleCourier,
		Guards: []Guard{GuardBeforeAcceptanceDeadline}, Emits: []EventName{ErrandAccepted}},
	{From: StatusOpen, Event: EventCancel, To: StatusCancelled, Actor: RelRequester,
		Credit: CreditRelease, Emits: []EventName{ErrandCancelled}},
	{From: StatusOpen, Event: EventAcceptanceDeadlinePassed, To: StatusExpired, Actor: RelSystem,
		Guards: []Guard{GuardAfterAcceptanceDeadline}, Credit: CreditRelease, Emits: []EventName{ErrandExpired}},

	{From: StatusAssigned, Event: EventWithdraw, To: StatusOpen, Actor: RelAssignedCourier,
		Guards: []Guard{GuardBeforeAcceptanceDeadline}, Emits: []EventName{ErrandWithdrawn}},
	{From: StatusAssigned, Event: EventWithdraw, To: StatusExpired, Actor: RelAssignedCourier,
		Guards: []Guard{GuardAfterAcceptanceDeadline}, Credit: CreditRelease,
		Emits: []EventName{ErrandWithdrawn, ErrandExpired}},
	{From: StatusAssigned, Event: EventPickupDeadlinePassed, To: StatusOpen, Actor: RelSystem,
		Guards: []Guard{GuardAfterPickupDeadline, GuardBeforeAcceptanceDeadline}, Emits: []EventName{ErrandWithdrawn}},
	{From: StatusAssigned, Event: EventPickupDeadlinePassed, To: StatusExpired, Actor: RelSystem,
		Guards: []Guard{GuardAfterPickupDeadline, GuardAfterAcceptanceDeadline}, Credit: CreditRelease,
		Emits: []EventName{ErrandWithdrawn, ErrandExpired}},
	{From: StatusAssigned, Event: EventPickUp, To: StatusPickedUp, Actor: RelAssignedCourier,
		Emits: []EventName{ErrandPickedUp}},

	{From: StatusPickedUp, Event: EventDeliver, To: StatusDelivered, Actor: RelAssignedCourier,
		Emits: []EventName{ErrandDelivered}},
	{From: StatusPickedUp, Event: EventReportIssue, To: StatusReviewRequired, Actor: RelAssignedCourier,
		Review: ReviewCourierIssue, NeedsReason: true, Emits: []EventName{ErrandReviewRequired}},
	{From: StatusPickedUp, Event: EventDeliveryDeadlinePassed, To: StatusReviewRequired, Actor: RelSystem,
		Guards: []Guard{GuardAfterDeliveryDeadline}, Review: ReviewDeliveryOverdue,
		Emits: []EventName{ErrandReviewRequired}},

	{From: StatusDelivered, Event: EventConfirm, To: StatusCompleted, Actor: RelRequester,
		Credit: CreditTransfer, Emits: []EventName{ErrandCompleted}},
	{From: StatusDelivered, Event: EventVerificationLapsed, To: StatusCompleted, Actor: RelSystem,
		Guards: []Guard{GuardAfterVerificationDeadline}, Credit: CreditTransfer, Emits: []EventName{ErrandCompleted}},
	{From: StatusDelivered, Event: EventDispute, To: StatusReviewRequired, Actor: RelRequester,
		Review: ReviewRequesterDispute, NeedsReason: true, Emits: []EventName{ErrandReviewRequired}},

	{From: StatusReviewRequired, Event: EventAdminComplete, To: StatusCompleted, Actor: RelAdmin,
		NeedsReason: true, Credit: CreditTransfer, Emits: []EventName{ErrandCompleted}},
	{From: StatusReviewRequired, Event: EventAdminRefund, To: StatusRefunded, Actor: RelAdmin,
		NeedsReason: true, Credit: CreditRelease, Emits: []EventName{ErrandRefunded}},
}

// Rules returns a copy of the transition table. Any (status, event) pair not
// in it is invalid (NFR6.1.1).
func Rules() []Rule {
	rules := make([]Rule, len(transitionTable))
	copy(rules, transitionTable)
	return rules
}

// RulesFor returns the rules for one (status, event) pair, empty if the pair
// is invalid.
func RulesFor(from Status, ev Event) []Rule {
	var rules []Rule
	for _, r := range transitionTable {
		if r.From == from && r.Event == ev {
			rules = append(rules, r)
		}
	}
	return rules
}

// Next decides the transition for event ev on errand e at now. It is pure:
// history and settings come in through input, and the caller persists the
// result with Store.Apply (or Store.Create for Submit, where e is the new
// errand with Status StatusNone and Version 0).
//
// Checks run in this order, and the first failure is returned as an
// *apperror.Error:
//
//  1. ERRAND_UNAVAILABLE if e is terminal.
//  2. If the pair is not in the table: ERRAND_UNAVAILABLE if the errand has
//     moved past every state the event can leave (for example accepting an
//     errand someone else accepted, F3.3.7), otherwise INVALID_TRANSITION.
//  3. FORBIDDEN if the actor lacks the rule's relationship to the errand
//     (NFR3.3.2); for Accept, NOT_ELIGIBLE or COURIER_RESTRICTED if the
//     courier fails F3.3.6.
//  4. Deadline guards: an accept at or after the acceptance deadline is
//     ERRAND_UNAVAILABLE (F3.3.4); a deadline event before its deadline is
//     INVALID_TRANSITION.
//  5. Event input: VALIDATION_FAILED for a bad edit, submission or missing
//     reason; EDIT_PENDING for an edit while another is pending;
//     ERRAND_CHANGED for an edit outcome that does not match the pending
//     edit.
func Next(e Errand, ev Event, actor Actor, input EventInput, now time.Time) (Transition, error) {
	if e.Status != StatusNone && !e.Status.Valid() {
		return Transition{}, newError(apperror.InvalidArgument, "unknown errand status %q", e.Status)
	}
	if e.Status.Terminal() {
		return Transition{}, newError(apperror.ErrandUnavailable, "errand is %s", e.Status)
	}
	candidates := RulesFor(e.Status, ev)
	if len(candidates) == 0 {
		if movedPast(e.Status, ev) {
			return Transition{}, newError(apperror.ErrandUnavailable, "errand is %s and can no longer %s", e.Status, ev)
		}
		return Transition{}, newError(apperror.InvalidTransition, "cannot %s an errand that is %s", ev, displayStatus(e.Status))
	}
	if err := checkActor(candidates[0].Actor, e, actor, input); err != nil {
		return Transition{}, err
	}
	rule, err := chooseRule(candidates, e, now)
	if err != nil {
		return Transition{}, err
	}

	t := Transition{
		ErrandID:        e.ID,
		From:            e.Status,
		To:              rule.To,
		Event:           ev,
		Actor:           actor,
		Reason:          input.Reason,
		ExpectedVersion: e.Version,
		NewVersion:      e.Version + 1,
		At:              now,
	}
	if e.PendingEdit != nil {
		t.ExpectedPendingEditKey = e.PendingEdit.Key
	}
	if rule.NeedsReason && isBlank(input.Reason) {
		return Transition{}, validationError(apperror.Field{Field: "reason", Message: "is required"})
	}
	if rule.Review != "" {
		t.Changes.ReviewReason = setTo(rule.Review)
	}
	if err := applyEvent(&t, e, input, now); err != nil {
		return Transition{}, err
	}
	// Leaving OPEN drops a pending edit (F3.4.2, A5). Its adjustment is
	// reverted by accept and covered by the release otherwise.
	if e.PendingEdit != nil && t.To != StatusOpen {
		t.Changes.PendingEdit = setNull[PendingEdit]()
	}
	t.Outbox = outboxRows(rule, e, t, input)
	return t, nil
}

// applyEvent fills in the event-specific parts of t.
func applyEvent(t *Transition, e Errand, input EventInput, now time.Time) error {
	switch t.Event {
	case EventSubmit:
		if e.ID == "" {
			return newError(apperror.InvalidArgument, "errand ID is required")
		}
		if e.Version != 0 {
			return newError(apperror.InvalidArgument, "a new errand has no version")
		}
		if err := validateDetails(e.Details, now, true); err != nil {
			return err
		}
		t.ExpectedVersion = 0
		t.NewVersion = 1

	case EventReservationRefused:
		t.Reason = orDefault(t.Reason, ReasonInsufficientCredits)
	case EventReservationFailed:
		t.Reason = orDefault(t.Reason, ReasonReservationFailed)

	case EventEdit:
		if e.PendingEdit != nil {
			return newError(apperror.EditPending, "a reward change is still waiting for credit-service")
		}
		if input.Edit == nil {
			return newError(apperror.InvalidArgument, "edit details are required")
		}
		edit := *input.Edit
		// The 10 min-24 h window applies only to a new deadline; an
		// unchanged one just must not have passed.
		deadlineChanged := !edit.AcceptanceDeadline.Equal(e.Details.AcceptanceDeadline)
		if err := validateDetails(edit, now, deadlineChanged); err != nil {
			return err
		}
		if edit.Reward == e.Details.Reward {
			t.Changes.Details = &edit
			return nil
		}
		// A reward change waits for credit-service (F4.4.3). The version is
		// kept, so couriers can still accept the current details.
		t.NewVersion = e.Version
		t.Changes.PendingEdit = setTo(PendingEdit{
			Details: edit,
			Key:     IdempotencyKey(e.ID, "adjust", e.Version),
			At:      now,
		})

	case EventEditConfirmed, EventEditRefused:
		if e.PendingEdit == nil || input.EditKey != e.PendingEdit.Key {
			return newError(apperror.ErrandChanged, "no pending edit matches %q", input.EditKey)
		}
		t.Changes.PendingEdit = setNull[PendingEdit]()
		if t.Event == EventEditConfirmed {
			details := e.PendingEdit.Details
			t.Changes.Details = &details
		} else if input.EditTimedOut {
			t.Reason = orDefault(t.Reason, ReasonEditFailed)
		} else {
			t.Reason = orDefault(t.Reason, ReasonInsufficientCredits)
		}

	case EventAccept:
		if err := input.Deadlines.Validate(); err != nil {
			return err
		}
		t.Changes.CourierID = setTo(t.Actor.ID)
		t.Changes.PickupDeadline = setTo(input.Deadlines.PickupDeadline(now))
		t.Changes.DeliveryDeadline = setTo(input.Deadlines.DeliveryDeadline(now))

	case EventWithdraw, EventPickupDeadlinePassed:
		t.Changes.CourierID = setNull[string]()
		t.Changes.PickupDeadline = setNull[time.Time]()
		t.Changes.DeliveryDeadline = setNull[time.Time]()

	case EventDeliver:
		if err := input.Deadlines.Validate(); err != nil {
			return err
		}
		t.Changes.DeliveredAt = setTo(now)
		t.Changes.VerificationDeadline = setTo(input.Deadlines.VerificationDeadline(now))
	}
	return nil
}

func checkActor(rel Relationship, e Errand, actor Actor, input EventInput) error {
	forbidden := newError(apperror.Forbidden, "only the errand's %s may do this", rel)
	switch rel {
	case RelSystem:
		if actor.Type != ActorSystem || actor.ID != "" {
			return forbidden
		}
		return nil
	case RelAdmin:
		if actor.Type != ActorAdmin || actor.ID == "" {
			return forbidden
		}
		return nil
	case RelRequester:
		if actor.Type != ActorRequester || actor.ID == "" || actor.ID != e.RequesterID {
			return forbidden
		}
		return nil
	case RelAssignedCourier:
		if actor.Type != ActorCourier || actor.ID == "" || actor.ID != e.CourierID {
			return forbidden
		}
		return nil
	case RelEligibleCourier:
		if actor.Type != ActorCourier || actor.ID == "" {
			return newError(apperror.Forbidden, "only a courier may accept an errand")
		}
		switch {
		case actor.ID == e.RequesterID:
			return newError(apperror.NotEligible, "you cannot accept your own errand")
		case input.PreviouslyWithdrew:
			return newError(apperror.NotEligible, "you cannot accept an errand you withdrew from")
		case input.Restricted:
			return newError(apperror.CourierRestricted, "you are temporarily restricted from accepting errands")
		}
		return nil
	default:
		return newError(apperror.Internal, "unknown relationship %q", rel)
	}
}

// chooseRule returns the first candidate whose guards all hold at now.
func chooseRule(candidates []Rule, e Errand, now time.Time) (Rule, error) {
	var failed Guard
	for _, r := range candidates {
		ok := true
		for _, g := range r.Guards {
			if !guardHolds(g, e, now) {
				failed, ok = g, false
				break
			}
		}
		if ok {
			return r, nil
		}
	}
	if failed == GuardBeforeAcceptanceDeadline {
		return Rule{}, newError(apperror.ErrandUnavailable, "the errand's acceptance deadline has passed")
	}
	return Rule{}, newError(apperror.InvalidTransition, "the guard %q does not hold yet", failed)
}

func guardHolds(g Guard, e Errand, now time.Time) bool {
	passed := func(deadline *time.Time) bool { return deadline != nil && Passed(*deadline, now) }
	switch g {
	case GuardBeforeAcceptanceDeadline:
		return !Passed(e.Details.AcceptanceDeadline, now)
	case GuardAfterAcceptanceDeadline:
		return Passed(e.Details.AcceptanceDeadline, now)
	case GuardAfterPickupDeadline:
		return passed(e.PickupDeadline)
	case GuardAfterDeliveryDeadline:
		return passed(e.DeliveryDeadline)
	case GuardAfterVerificationDeadline:
		return passed(e.VerificationDeadline)
	default:
		return false
	}
}

// lifecycleRank orders the active states along the main path, so Next can
// tell "this errand has moved on" from "this errand is not there yet".
func lifecycleRank(s Status) int {
	switch s {
	case StatusNone:
		return 0
	case StatusPendingReservation:
		return 1
	case StatusOpen:
		return 2
	case StatusAssigned:
		return 3
	case StatusPickedUp:
		return 4
	case StatusDelivered:
		return 5
	case StatusReviewRequired:
		return 6
	default:
		return 7
	}
}

// movedPast reports whether every state ev can leave comes before current.
func movedPast(current Status, ev Event) bool {
	latest := -1
	for _, r := range transitionTable {
		if r.Event == ev {
			latest = max(latest, lifecycleRank(r.From))
		}
	}
	return latest >= 0 && lifecycleRank(current) > latest
}

// validateDetails checks d at now. The acceptance-deadline window applies
// only when newDeadline is set; otherwise the deadline must not have passed.
func validateDetails(d Details, now time.Time, newDeadline bool) error {
	var fields []apperror.Field
	if d.Reward <= 0 {
		fields = append(fields, apperror.Field{Field: "reward", Message: "must be a positive integer"})
	}
	var items []json.RawMessage
	if err := json.Unmarshal(d.Items, &items); err != nil || len(items) == 0 {
		fields = append(fields, apperror.Field{Field: "items", Message: "must list at least one item"})
	}
	if newDeadline {
		if field, ok := acceptanceDeadlineProblem(d.AcceptanceDeadline, now); !ok {
			fields = append(fields, field)
		}
	} else if Passed(d.AcceptanceDeadline, now) {
		fields = append(fields, apperror.Field{Field: "acceptanceDeadline", Message: "has already passed"})
	}
	if len(fields) > 0 {
		return validationError(fields...)
	}
	return nil
}

func displayStatus(s Status) string {
	if s == StatusNone {
		return "not yet created"
	}
	return string(s)
}

func isBlank(s string) bool { return strings.TrimSpace(s) == "" }

func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func newError(code apperror.Code, format string, args ...any) error {
	return &apperror.Error{Code: code, Message: fmt.Sprintf(format, args...)}
}
