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
	ErrandEnding(ctx context.Context, errandID uuid.UUID) (*ErrandEnding, error)
	EndRoom(ctx context.Context, roomID uuid.UUID, ending Ending) (Room, bool, error)
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
		ended, changed, err := tx.EndRoom(ctx, room.ID, Ending{
			Reason:   ending.Reason,
			EndedAt:  ending.EndedAt,
			ClosesAt: ending.EndedAt.Add(l.closeDelay),
		})
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
	switch {
	case e.ErrandID == uuid.Nil:
		return fmt.Errorf("%w: errandId is required", ErrInvalidEvent)
	case e.ErrandVersion < 1:
		return fmt.Errorf("%w: errandVersion must be 1 or more", ErrInvalidEvent)
	case e.RequesterID == "" || e.CourierID == "":
		return fmt.Errorf("%w: requesterId and courierId are required", ErrInvalidEvent)
	case e.RequesterID == e.CourierID:
		return fmt.Errorf("%w: requesterId and courierId must differ", ErrInvalidEvent)
	case e.OccurredAt.IsZero():
		return fmt.Errorf("%w: occurredAt is required", ErrInvalidEvent)
	}
	return nil
}
