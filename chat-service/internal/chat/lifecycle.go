// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-10
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

package chat

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Tx is what Lifecycle needs inside one database transaction.
// internal/store's *Queries implements it.
type Tx interface {
	// LockErrand makes one errand's events apply one at a time, even across
	// copies of the service, until the transaction ends.
	LockErrand(ctx context.Context, errandID uuid.UUID) error
	UpsertRoomOnAccept(ctx context.Context, room AcceptedRoom) (Room, bool, error)
	// EnsureRoom returns the (errand, courier) room, creating it without an
	// OpenedVersion when errand.withdrawn arrives before errand.accepted.
	EnsureRoom(ctx context.Context, errandID uuid.UUID, requesterID, courierID string) (Room, error)
	ErrandEnding(ctx context.Context, errandID uuid.UUID) (*ErrandEnding, error)
	// RecordErrandEnding remembers a completion or refund, keeping the
	// earliest, so a late errand.accepted creates its room already ending.
	RecordErrandEnding(ctx context.Context, ending ErrandEnding) error
	// EndRoom and EndRoomsForErrand keep the earliest ending: a room that
	// already ended at the same time or earlier is left alone. changed (or
	// the returned list) reports the rooms this call did change.
	EndRoom(ctx context.Context, roomID uuid.UUID, ending Ending) (Room, bool, error)
	EndRoomsForErrand(ctx context.Context, errandID uuid.UUID, ending Ending) ([]Room, error)
}

// LifecycleStore runs fn in one transaction: everything fn does is saved
// together, or nothing is.
type LifecycleStore interface {
	InTx(ctx context.Context, fn func(tx Tx) error) error
}

// Lifecycle keeps rooms in step with the errand events order-service
// publishes. Events arrive at least once and possibly out of order, so every
// handler is idempotent: applying an event twice changes nothing the second
// time.
type Lifecycle struct {
	store      LifecycleStore
	closeDelay time.Duration
	notifier   Notifier
}

// NewLifecycle makes a Lifecycle; a nil notifier pushes nothing.
func NewLifecycle(store LifecycleStore, closeDelay time.Duration, notifier Notifier) *Lifecycle {
	if store == nil {
		panic("chat: lifecycle store is required")
	}
	if closeDelay <= 0 {
		panic("chat: close delay must be positive")
	}
	if notifier == nil {
		notifier = NoNotifier{}
	}
	return &Lifecycle{store: store, closeDelay: closeDelay, notifier: notifier}
}

// AcceptedEvent is errand.accepted: courier CourierID accepted errand
// ErrandID, which RequesterID created.
type AcceptedEvent struct {
	ErrandID      uuid.UUID
	ErrandVersion int
	RequesterID   string
	CourierID     string
	OccurredAt    time.Time
}

// AcceptResult says what OnAccepted changed. After the commit, OnAccepted has
// told the browsers: room_opened when Opened, room_closing when Closing.
type AcceptResult struct {
	Room Room
	// Opened is true when this event created the room; false for a repeat.
	Opened bool
	// Closing is true when the errand had already completed or been refunded
	// (that event arrived first) and the room was created already ending.
	Closing bool
}

// ErrInvalidEvent: the event breaks the contract and can never be applied,
// so it should go to the dead-letter queue instead of being retried.
var ErrInvalidEvent = errors.New("invalid event")

// OnAccepted creates the room for a courier's acceptance. A repeat of the
// event changes nothing. If the errand's completion or refund was processed
// first (RabbitMQ may reorder), the room is created already ending at that
// time.
func (l *Lifecycle) OnAccepted(ctx context.Context, event AcceptedEvent) (AcceptResult, error) {
	if err := event.validate(); err != nil {
		return AcceptResult{}, err
	}
	var result AcceptResult
	err := l.store.InTx(ctx, func(tx Tx) error {
		result = AcceptResult{}
		if err := tx.LockErrand(ctx, event.ErrandID); err != nil {
			return err
		}
		room, inserted, err := tx.UpsertRoomOnAccept(ctx, AcceptedRoom{
			ErrandID:      event.ErrandID,
			RequesterID:   event.RequesterID,
			CourierID:     event.CourierID,
			OpenedVersion: event.ErrandVersion,
		})
		if err != nil {
			return err
		}
		result.Room, result.Opened = room, inserted

		ending, err := tx.ErrandEnding(ctx, event.ErrandID)
		if err != nil || ending == nil {
			return err
		}
		ended, changed, err := tx.EndRoom(ctx, room.ID, l.ending(ending.Reason, ending.EndedAt))
		if err != nil {
			return err
		}
		result.Room, result.Closing = ended, changed
		return nil
	})
	if errors.Is(err, ErrInvalidRoom) {
		// For example requester and courier are the same person: retrying
		// can never succeed.
		return AcceptResult{}, fmt.Errorf("%w: %w", ErrInvalidEvent, err)
	}
	if err != nil {
		return AcceptResult{}, err
	}
	// Committed: now it is safe to tell the browsers. A repeat of the event
	// changed nothing, so it tells nobody.
	if result.Opened {
		l.notifier.RoomOpened(ctx, result.Room)
	}
	if result.Closing {
		l.notifier.RoomClosing(ctx, result.Room)
	}
	return result, nil
}

func (e AcceptedEvent) validate() error {
	return validateAssignment(e.ErrandID, e.ErrandVersion, e.RequesterID, e.CourierID, e.OccurredAt)
}

// validateAssignment checks the fields of an event about one courier's
// assignment (errand.accepted, errand.withdrawn).
func validateAssignment(errandID uuid.UUID, version int, requesterID, courierID string, occurredAt time.Time) error {
	switch {
	case errandID == uuid.Nil:
		return fmt.Errorf("%w: errandId is required", ErrInvalidEvent)
	case version < 1:
		return fmt.Errorf("%w: errandVersion must be 1 or more", ErrInvalidEvent)
	case requesterID == "" || courierID == "":
		return fmt.Errorf("%w: requesterId and courierId are required", ErrInvalidEvent)
	case requesterID == courierID:
		return fmt.Errorf("%w: requesterId and courierId must differ", ErrInvalidEvent)
	case occurredAt.IsZero():
		return fmt.Errorf("%w: occurredAt is required", ErrInvalidEvent)
	}
	return nil
}

// WithdrawnEvent is errand.withdrawn: courier CourierID withdrew from errand
// ErrandID, or was removed at the pickup deadline. It ends only that
// courier's room.
type WithdrawnEvent struct {
	ErrandID      uuid.UUID
	ErrandVersion int
	RequesterID   string
	CourierID     string
	OccurredAt    time.Time
}

// WithdrawResult says what OnWithdrawn changed.
type WithdrawResult struct {
	Room Room
	// Closing is true when this event set the room's ending or moved it
	// earlier; after the commit both participants were sent room_closing.
	// False for a repeat.
	Closing bool
}

// OnWithdrawn ends the withdrawing courier's room: it stays writable for the
// close delay after the withdrawal, then becomes read-only. If the
// withdrawal is processed before the acceptance (RabbitMQ may reorder), the
// room is created here already ending, and the late errand.accepted cannot
// reopen it.
func (l *Lifecycle) OnWithdrawn(ctx context.Context, event WithdrawnEvent) (WithdrawResult, error) {
	if err := validateAssignment(event.ErrandID, event.ErrandVersion, event.RequesterID, event.CourierID, event.OccurredAt); err != nil {
		return WithdrawResult{}, err
	}
	var result WithdrawResult
	err := l.store.InTx(ctx, func(tx Tx) error {
		result = WithdrawResult{}
		if err := tx.LockErrand(ctx, event.ErrandID); err != nil {
			return err
		}
		room, err := tx.EnsureRoom(ctx, event.ErrandID, event.RequesterID, event.CourierID)
		if err != nil {
			return err
		}
		ended, changed, err := tx.EndRoom(ctx, room.ID, l.ending(EndWithdrawn, event.OccurredAt))
		if err != nil {
			return err
		}
		result = WithdrawResult{Room: ended, Closing: changed}
		return nil
	})
	if errors.Is(err, ErrInvalidRoom) {
		return WithdrawResult{}, fmt.Errorf("%w: %w", ErrInvalidEvent, err)
	}
	if err != nil {
		return WithdrawResult{}, err
	}
	if result.Closing {
		l.notifier.RoomClosing(ctx, result.Room)
	}
	return result, nil
}

// ErrandEndedEvent is errand.completed or errand.refunded: the whole errand
// ended, so every room of it ends.
type ErrandEndedEvent struct {
	ErrandID      uuid.UUID
	ErrandVersion int
	// Reason is EndCompleted or EndRefunded.
	Reason     EndReason
	OccurredAt time.Time
}

// EndResult says what OnErrandEnded changed.
type EndResult struct {
	// Closing lists the rooms this event ended or ended earlier; after the
	// commit each one's participants were sent room_closing. Rooms that had
	// already ended (a withdrawn courier's room, or a repeat of the event)
	// are not listed.
	Closing []Room
}

// OnErrandEnded ends every room of a completed or refunded errand. A room
// that already ended earlier, such as a withdrawn courier's, keeps its
// earlier ending. The ending is also recorded on its own, so a room created
// later by a late errand.accepted starts already ending.
func (l *Lifecycle) OnErrandEnded(ctx context.Context, event ErrandEndedEvent) (EndResult, error) {
	if err := event.validate(); err != nil {
		return EndResult{}, err
	}
	var result EndResult
	err := l.store.InTx(ctx, func(tx Tx) error {
		result = EndResult{}
		if err := tx.LockErrand(ctx, event.ErrandID); err != nil {
			return err
		}
		if err := tx.RecordErrandEnding(ctx, ErrandEnding{
			ErrandID: event.ErrandID, Reason: event.Reason, EndedAt: event.OccurredAt,
		}); err != nil {
			return err
		}
		rooms, err := tx.EndRoomsForErrand(ctx, event.ErrandID, l.ending(event.Reason, event.OccurredAt))
		if err != nil {
			return err
		}
		result.Closing = rooms
		return nil
	})
	if errors.Is(err, ErrInvalidRoom) {
		return EndResult{}, fmt.Errorf("%w: %w", ErrInvalidEvent, err)
	}
	if err != nil {
		return EndResult{}, err
	}
	for _, room := range result.Closing {
		l.notifier.RoomClosing(ctx, room)
	}
	return result, nil
}

// ending is the Ending for an errand that ended at endedAt. The time is the
// event's occurredAt, not when it was processed, so a late event gives the
// same result.
func (l *Lifecycle) ending(reason EndReason, endedAt time.Time) Ending {
	return Ending{Reason: reason, EndedAt: endedAt, ClosesAt: endedAt.Add(l.closeDelay)}
}

func (e ErrandEndedEvent) validate() error {
	switch {
	case e.ErrandID == uuid.Nil:
		return fmt.Errorf("%w: errandId is required", ErrInvalidEvent)
	case e.ErrandVersion < 1:
		return fmt.Errorf("%w: errandVersion must be 1 or more", ErrInvalidEvent)
	case e.Reason != EndCompleted && e.Reason != EndRefunded:
		return fmt.Errorf("%w: an errand ends only by completion or refund", ErrInvalidEvent)
	case e.OccurredAt.IsZero():
		return fmt.Errorf("%w: occurredAt is required", ErrInvalidEvent)
	}
	return nil
}
