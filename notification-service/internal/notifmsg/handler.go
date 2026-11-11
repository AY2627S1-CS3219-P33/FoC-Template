// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: Implementation of notification-service notification event messaging.
// Author review: I validated correctness, edited for style, and added boundary checks.

package notifmsg

import (
	"context"
	"errors"

	"github.com/CS3219-AY2627S1/FoC-Template/notification-service/internal/notification"
)

// Routing keys of the errand events (foc.events). notification-service binds
// errand.# and recognises the keys below; any other errand.* key is treated as
// undecodable and dead-lettered, as the contract requires.
const (
	EventPlaced                    = "errand.placed"
	EventOpened                    = "errand.opened"
	EventRejected                  = "errand.rejected"
	EventEdited                    = "errand.edited"
	EventEditRequested             = "errand.edit_requested"
	EventEditRejected              = "errand.edit_rejected"
	EventAccepted                  = "errand.accepted"
	EventCancelled                 = "errand.cancelled"
	EventWithdrawn                 = "errand.withdrawn"
	EventExpired                   = "errand.expired"
	EventPickedUp                  = "errand.picked_up"
	EventDelivered                 = "errand.delivered"
	EventReviewRequired            = "errand.review_required"
	EventCompleted                 = "errand.completed"
	EventRefunded                  = "errand.refunded"
	EventAcceptRejected            = "errand.accept_rejected"
	EventCancelRejected            = "errand.cancel_rejected"
	EventPickupRejected            = "errand.pickup_rejected"
	EventVerificationReminder      = "errand.verification_reminder"
	EventPickupDeadlineApproaching = "errand.pickup_deadline_approaching"

	actorRequester = "REQUESTER"
	actorCourier   = "COURIER"
)

// Recorder stores the notifications derived from one event. The event's
// messageId makes the write idempotent across redeliveries.
type Recorder interface {
	Record(ctx context.Context, messageID string, items []notification.Pending) (int, error)
}

// Handler turns one errand event into stored notifications. It is the
// MessageHandler the Consumer drives.
type Handler struct {
	recorder Recorder
}

func NewHandler(recorder Recorder) Handler {
	if recorder == nil {
		panic("notifmsg: recorder is required")
	}
	return Handler{recorder: recorder}
}

// Handle decodes one event and stores a notification for each recipient it
// addresses. Recognised events with no recipient (for example errand.placed,
// which is internal and not yet an available errand) are acknowledged without
// storing anything. When Handle returns an error, the consumer uses
// DispositionOf to decide whether to dead-letter or retry the message.
func (h Handler) Handle(ctx context.Context, routingKey string, body []byte) error {
	env, err := decodeEnvelope(routingKey, body)
	if err != nil {
		return err
	}
	items, err := classify(env)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	_, err = h.recorder.Record(ctx, env.MessageID, items)
	return err
}

// classify maps an event to the notifications it produces. An unknown routing
// key is undecodable so it is dead-lettered rather than retried forever.
func classify(env Envelope) ([]notification.Pending, error) {
	switch env.Type {
	case EventPlaced:
		// Internal: the errand is PENDING_RESERVATION and not yet available.
		return nil, nil
	case EventAcceptRejected, EventCancelRejected, EventPickupRejected:
		return classifyRejection(env)
	case EventVerificationReminder, EventPickupDeadlineApproaching:
		return classifyReminder(env)
	default:
		return classifyLifecycle(env)
	}
}

// message is the title and body shown for one event.
type message struct {
	title string
	body  string
}

// audience selects which parties a lifecycle notification addresses.
type audience int

const (
	requesterOnly audience = iota
	courierOnly
	bothParties
)

// lifecycle describes each recognised lifecycle event: who it addresses and
// what it says. The actor is removed from the recipients in classifyLifecycle,
// so a party is never told about an action it took itself.
var lifecycle = map[string]struct {
	to  audience
	msg message
}{
	EventOpened:         {requesterOnly, message{"Your errand is live", "Your errand has been posted and couriers can now accept it."}},
	EventRejected:       {requesterOnly, message{"Your errand was not posted", "We couldn't reserve the reward for your errand, so it was not posted."}},
	EventEdited:         {requesterOnly, message{"Your errand was updated", "Your changes to the errand have been applied."}},
	EventEditRequested:  {requesterOnly, message{"Your reward change is pending", "Your reward change is awaiting confirmation before it takes effect."}},
	EventEditRejected:   {requesterOnly, message{"Your reward change was rejected", "Your reward change could not be applied, so the original reward stands."}},
	EventAccepted:       {requesterOnly, message{"A courier accepted your errand", "A courier has accepted your errand and will pick it up soon."}},
	EventCancelled:      {bothParties, message{"An errand was cancelled", "An errand you were involved in has been cancelled."}},
	EventWithdrawn:      {requesterOnly, message{"A courier withdrew", "A courier withdrew from your errand, which is open for others again."}},
	EventExpired:        {bothParties, message{"An errand expired", "An errand expired before it could be completed."}},
	EventPickedUp:       {requesterOnly, message{"Your errand was picked up", "The courier has picked up your errand and is on the way."}},
	EventDelivered:      {requesterOnly, message{"Your errand was delivered", "The courier marked your errand delivered. Confirm it to release the reward."}},
	EventReviewRequired: {bothParties, message{"Your errand needs review", "An errand needs review before it can be settled. An administrator will take a look."}},
	EventCompleted:      {bothParties, message{"Errand completed", "The errand is complete and the reward has been settled."}},
	EventRefunded:       {bothParties, message{"Errand refunded", "An errand was refunded and the reward returned to the requester."}},
}

func classifyLifecycle(env Envelope) ([]notification.Pending, error) {
	spec, ok := lifecycle[env.Type]
	if !ok {
		return nil, undecodable("unknown routing key %q", env.Type)
	}
	var d errandEventData
	if err := decodeData(env, &d); err != nil {
		return nil, err
	}
	if d.RequesterID == "" {
		return nil, undecodable("requesterId is missing for %s", env.Type)
	}

	var recipients []string
	switch spec.to {
	case requesterOnly:
		recipients = []string{d.RequesterID}
	case courierOnly:
		if d.CourierID != "" {
			recipients = []string{d.CourierID}
		}
	case bothParties:
		recipients = []string{d.RequesterID}
		if d.CourierID != "" {
			recipients = append(recipients, d.CourierID)
		}
	}

	// A party is never notified about an action it performed itself. System
	// and admin actions have no such party to exclude.
	if d.ActorID != "" && (d.ActorType == actorRequester || d.ActorType == actorCourier) {
		recipients = remove(recipients, d.ActorID)
	}
	return build(env, recipients, spec.msg), nil
}

func classifyRejection(env Envelope) ([]notification.Pending, error) {
	var d rejectionData
	if err := decodeData(env, &d); err != nil {
		return nil, err
	}
	if d.ActorID == "" {
		// Nobody to tell: the actor is the only recipient of a failed action.
		return nil, nil
	}
	action := map[string]string{"accept": "accept", "cancel": "cancel", "pickup": "pick up"}[d.Action]
	if action == "" {
		action = "update"
	}
	reason := map[string]string{
		"ERRAND_CHANGED":     "The errand changed before your request went through.",
		"ERRAND_UNAVAILABLE": "The errand was no longer available.",
		"INVALID_TRANSITION": "The errand was not in a state that allows it.",
	}[d.Code]
	if reason == "" {
		reason = "The errand could not be updated."
	}
	msg := message{
		title: "Couldn't " + action + " the errand",
		body:  reason + " Please refresh and try again.",
	}
	return build(env, []string{d.ActorID}, msg), nil
}

func classifyReminder(env Envelope) ([]notification.Pending, error) {
	var d reminderData
	if err := decodeData(env, &d); err != nil {
		return nil, err
	}
	switch env.Type {
	case EventVerificationReminder:
		if d.RequesterID == "" {
			return nil, nil
		}
		return build(env, []string{d.RequesterID}, message{
			"Confirm your delivery",
			"Please confirm your errand was delivered before the deadline, or it completes automatically.",
		}), nil
	case EventPickupDeadlineApproaching:
		if d.CourierID == "" {
			return nil, nil
		}
		return build(env, []string{d.CourierID}, message{
			"Pickup deadline approaching",
			"The pickup deadline for an errand you accepted is near. Pick it up soon to keep it.",
		}), nil
	default:
		return nil, undecodable("unknown reminder %q", env.Type)
	}
}

// build makes one Pending per recipient. CreatedAt is the event time so a
// user sees notifications in the order things happened, not the order they
// were delivered.
func build(env Envelope, recipients []string, msg message) []notification.Pending {
	items := make([]notification.Pending, 0, len(recipients))
	for _, recipient := range recipients {
		items = append(items, notification.Pending{
			Recipient: recipient,
			ErrandID:  env.ErrandID,
			Type:      env.Type,
			Title:     msg.title,
			Body:      msg.body,
			CreatedAt: env.OccurredAt,
		})
	}
	return items
}

func remove(values []string, drop string) []string {
	out := values[:0]
	for _, v := range values {
		if v != drop {
			out = append(out, v)
		}
	}
	return out
}

// Disposition is what the consumer does with a message after Handle.
type Disposition int

const (
	// Ack: done. The notifications are stored (or there were none to store).
	Ack Disposition = iota
	// Reject: the message can never succeed. Reject without requeue so it
	// goes to the dead-letter queue.
	Reject
	// Requeue: a temporary failure such as the database being down. Nack with
	// requeue; the queue's delivery limit stops an endless loop.
	Requeue
)

// DispositionOf maps the error from Handle to what the consumer should do.
func DispositionOf(err error) Disposition {
	switch {
	case err == nil:
		return Ack
	case errors.Is(err, ErrUndecodable), errors.Is(err, notification.ErrInvalid):
		return Reject
	default:
		return Requeue
	}
}
