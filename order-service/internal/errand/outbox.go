package errand

import (
	"strconv"
	"time"
)

// Outbox row kinds for credit commands. The kind is also the command's
// routing key on foc.commands.
const (
	KindCreditReserve  = "credit.reserve"
	KindCreditAdjust   = "credit.adjust"
	KindCreditRelease  = "credit.release"
	KindCreditTransfer = "credit.transfer"
)

// EventName is a lifecycle event's routing key on foc.events
// (CS3219_new.drawio.xml). Its outbox row kind is "event." + the name.
type EventName string

const (
	ErrandPlaced         EventName = "errand.placed"
	ErrandOpened         EventName = "errand.opened"
	ErrandRejected       EventName = "errand.rejected"
	ErrandEdited         EventName = "errand.edited"
	ErrandEditRequested  EventName = "errand.edit_requested"
	ErrandEditRejected   EventName = "errand.edit_rejected"
	ErrandAccepted       EventName = "errand.accepted"
	ErrandCancelled      EventName = "errand.cancelled"
	ErrandWithdrawn      EventName = "errand.withdrawn"
	ErrandExpired        EventName = "errand.expired"
	ErrandPickedUp       EventName = "errand.picked_up"
	ErrandDelivered      EventName = "errand.delivered"
	ErrandReviewRequired EventName = "errand.review_required"
	ErrandCompleted      EventName = "errand.completed"
	ErrandRefunded       EventName = "errand.refunded"

	// Rejection events are not transitions. Store.RecordRejection writes
	// them for a refused accept, cancel or pickup.
	ErrandAcceptRejected EventName = "errand.accept_rejected"
	ErrandCancelRejected EventName = "errand.cancel_rejected"
	ErrandPickupRejected EventName = "errand.pickup_rejected"
)

// TransitionEventNames lists the events a transition may emit.
func TransitionEventNames() []EventName {
	return []EventName{
		ErrandPlaced, ErrandOpened, ErrandRejected, ErrandEdited,
		ErrandEditRequested, ErrandEditRejected, ErrandAccepted,
		ErrandCancelled, ErrandWithdrawn, ErrandExpired, ErrandPickedUp,
		ErrandDelivered, ErrandReviewRequired, ErrandCompleted, ErrandRefunded,
	}
}

// Kind is the outbox row kind for the event.
func (n EventName) Kind() string { return "event." + string(n) }

// operation is the name's last segment, used in idempotency keys
// ("errand.picked_up" → "picked_up").
func (n EventName) operation() string { return string(n)[len("errand."):] }

// OutboxRow is one row Store.Apply inserts with the transition. Payload is a
// CreditCommand for credit kinds and an EventData for event kinds; the store
// stores it as JSON.
type OutboxRow struct {
	Kind           string
	IdempotencyKey string
	ErrandID       string
	Payload        any
}

// IdempotencyKey formats {errandId}:{operation}[:{version}]. A version of 0
// is omitted, for operations that happen at most once per errand. The key is
// also the message's messageId.
func IdempotencyKey(errandID, operation string, version int) string {
	key := errandID + ":" + operation
	if version > 0 {
		key += ":" + strconv.Itoa(version)
	}
	return key
}

// AdjustSeq and RevertSeq are the seq values of credit.adjust commands.
// Credit-service ignores an adjust whose seq is not newer than the last one
// it applied, so seqs must strictly increase per errand. A reward-edit
// request does not change the version, and a revert at newVersion can be
// followed by an edit at that same version, so seq = version alone would
// repeat. Doubling interleaves them: an edit requested at version v sends
// 2v; a revert by the transition to version w sends 2w-1, which is above the
// cleared edit's 2(w-1) and below any later edit's 2w.
func AdjustSeq(version int) int { return 2 * version }

func RevertSeq(newVersion int) int { return 2*newVersion - 1 }

// CreditCommand is the payload of a credit.* row.
type CreditCommand struct {
	ErrandID      string `json:"errandId"`
	ErrandVersion int    `json:"errandVersion"`
	RequesterID   string `json:"requesterId"`
	// CourierID is the payee of a transfer.
	CourierID string `json:"courierId,omitempty"`
	// Amount is the reward to reserve or transfer. A release always returns
	// the whole reservation, so it carries no amount.
	Amount int `json:"amount,omitempty"`
	// NewAmount and Seq belong to credit.adjust: the absolute amount the
	// reservation should hold, and the ordering number from AdjustSeq or
	// RevertSeq.
	NewAmount int `json:"newAmount,omitempty"`
	Seq       int `json:"seq,omitempty"`
}

// EventData is the payload of an event.errand.* row: the envelope's data.
type EventData struct {
	ErrandID      string       `json:"errandId"`
	ErrandVersion int          `json:"errandVersion"`
	Event         Event        `json:"event"`
	From          Status       `json:"from,omitempty"`
	To            Status       `json:"to"`
	ActorType     ActorType    `json:"actorType"`
	ActorID       string       `json:"actorId,omitempty"`
	Reason        string       `json:"reason,omitempty"`
	ReviewReason  ReviewReason `json:"reviewReason,omitempty"`
	RequesterID   string       `json:"requesterId"`
	// CourierID is the courier involved, including one who has just
	// withdrawn or been withdrawn, so they can be notified (F5.1.3).
	CourierID string    `json:"courierId,omitempty"`
	Reward    int       `json:"reward"`
	At        time.Time `json:"at"`
	// PendingEdit is the requested edit on errand.edit_requested.
	PendingEdit *Details `json:"pendingEdit,omitempty"`
	// EditDropped is set on errand.accepted when the acceptance cleared a
	// pending edit, so the requester can be told (F3.4.2).
	EditDropped bool `json:"editDropped,omitempty"`
}

// outboxRows builds the rows for a transition that rule r allowed. before is
// the errand as Next received it; t is complete apart from Outbox.
func outboxRows(r Rule, before Errand, t Transition, input EventInput) []OutboxRow {
	after := t.ApplyTo(before)
	var rows []OutboxRow

	credit := func(kind, operation string, version int, cmd CreditCommand) {
		cmd.ErrandID = before.ID
		cmd.ErrandVersion = t.NewVersion
		cmd.RequesterID = before.RequesterID
		rows = append(rows, OutboxRow{
			Kind:           kind,
			IdempotencyKey: IdempotencyKey(before.ID, operation, version),
			ErrandID:       before.ID,
			Payload:        cmd,
		})
	}

	switch r.Credit {
	case CreditReserve:
		credit(KindCreditReserve, "reserve", 0, CreditCommand{Amount: after.Details.Reward})
	case CreditRelease:
		credit(KindCreditRelease, "release", 0, CreditCommand{})
	case CreditTransfer:
		credit(KindCreditTransfer, "transfer", 0, CreditCommand{
			CourierID: before.CourierID,
			Amount:    after.Details.Reward,
		})
	}

	editRequested := t.Event == EventEdit && t.Changes.PendingEdit.Set && t.Changes.PendingEdit.Value != nil
	if editRequested {
		pending := t.Changes.PendingEdit.Value
		credit(KindCreditAdjust, "adjust", t.ExpectedVersion, CreditCommand{
			NewAmount: pending.Details.Reward,
			Seq:       AdjustSeq(t.ExpectedVersion),
		})
	}
	if revertsAdjust(before, t, input) {
		credit(KindCreditAdjust, "adjust-revert", t.NewVersion, CreditCommand{
			NewAmount: before.Details.Reward,
			Seq:       RevertSeq(t.NewVersion),
		})
	}

	names := r.Emits
	if editRequested {
		names = []EventName{ErrandEditRequested}
	}
	for _, name := range names {
		data := EventData{
			ErrandID:      before.ID,
			ErrandVersion: t.NewVersion,
			Event:         t.Event,
			From:          t.From,
			To:            t.To,
			ActorType:     t.Actor.Type,
			ActorID:       t.Actor.ID,
			Reason:        t.Reason,
			ReviewReason:  after.ReviewReason,
			RequesterID:   before.RequesterID,
			CourierID:     before.CourierID,
			Reward:        after.Details.Reward,
			At:            t.At,
		}
		if data.CourierID == "" {
			data.CourierID = after.CourierID
		}
		if editRequested {
			details := t.Changes.PendingEdit.Value.Details
			data.PendingEdit = &details
		}
		if t.Event == EventAccept && before.PendingEdit != nil {
			data.EditDropped = true
		}
		rows = append(rows, OutboxRow{
			Kind:           name.Kind(),
			IdempotencyKey: IdempotencyKey(before.ID, name.operation(), t.NewVersion),
			ErrandID:       before.ID,
			Payload:        data,
		})
	}
	return rows
}

// revertsAdjust reports whether t clears a pending edit whose credit.adjust
// may have landed while the reservation stays held: an accept (F3.4.2) or a
// timed-out refusal. Cancel and expiry clear it too, but their release
// returns the whole reservation, so no revert is needed.
func revertsAdjust(before Errand, t Transition, input EventInput) bool {
	if before.PendingEdit == nil {
		return false
	}
	switch t.Event {
	case EventAccept:
		return true
	case EventEditRefused:
		return input.EditTimedOut
	default:
		return false
	}
}
