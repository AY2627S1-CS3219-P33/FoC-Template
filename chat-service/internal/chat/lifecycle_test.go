// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-10
// Scope: Implementation of test code (unit and integration tests).
// Author review: Reviewed, validated, and tested by the authors.

package chat_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/chat"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/store"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/testdb"
)

// These tests run against a real PostgreSQL (they skip without
// TEST_DATABASE_URL), because the lock, the upsert and the transaction are
// the point.

func accepted(errandID uuid.UUID, courier string, version int) chat.AcceptedEvent {
	return chat.AcceptedEvent{
		ErrandID: errandID, ErrandVersion: version,
		RequesterID: requester, CourierID: courier, OccurredAt: start,
	}
}

func TestOnAcceptedCreatesOneRoom(t *testing.T) {
	ctx := context.Background()
	rooms := store.New(testdb.Migrated(t).Pool)
	lifecycle := chat.NewLifecycle(rooms, 24*time.Hour, nil)
	errand := uuid.New()

	first, err := lifecycle.OnAccepted(ctx, accepted(errand, courierA, 3))
	require.NoError(t, err)
	require.True(t, first.Opened)
	require.False(t, first.Closing)
	require.Equal(t, errand, first.Room.ErrandID)
	require.Equal(t, requester, first.Room.RequesterID)
	require.Equal(t, courierA, first.Room.CourierID)
	require.Equal(t, 3, *first.Room.OpenedVersion)
	require.Nil(t, first.Room.Ending)

	again, err := lifecycle.OnAccepted(ctx, accepted(errand, courierA, 3))
	require.NoError(t, err, "RabbitMQ may deliver an event twice")
	require.False(t, again.Opened, "the repeat changes nothing")
	require.Equal(t, first.Room.ID, again.Room.ID)

	listed, err := rooms.ListRoomsForErrand(ctx, errand)
	require.NoError(t, err)
	require.Len(t, listed, 1)
}

// errand.completed arrived before errand.accepted (RabbitMQ may reorder):
// the room is created already ending at the completion time.
func TestOnAcceptedAfterCompletionCreatesAnEndingRoom(t *testing.T) {
	ctx := context.Background()
	rooms := store.New(testdb.Migrated(t).Pool)
	lifecycle := chat.NewLifecycle(rooms, 24*time.Hour, nil)
	errand := uuid.New()
	completedAt := start.Add(2 * time.Hour)
	require.NoError(t, rooms.RecordErrandEnding(ctx, chat.ErrandEnding{
		ErrandID: errand, Reason: chat.EndCompleted, EndedAt: completedAt,
	}))

	result, err := lifecycle.OnAccepted(ctx, accepted(errand, courierA, 2))
	require.NoError(t, err)
	require.True(t, result.Opened)
	require.True(t, result.Closing)
	require.NotNil(t, result.Room.Ending)
	require.Equal(t, chat.EndCompleted, result.Room.Ending.Reason)
	require.True(t, completedAt.Equal(result.Room.Ending.EndedAt))
	require.True(t, completedAt.Add(24*time.Hour).Equal(result.Room.Ending.ClosesAt))

	again, err := lifecycle.OnAccepted(ctx, accepted(errand, courierA, 2))
	require.NoError(t, err)
	require.False(t, again.Opened)
	require.False(t, again.Closing, "the ending is already there")
}

// errand.withdrawn arrived before errand.accepted and created the room
// already ended: the late accept fills in the version but cannot reopen it.
func TestOnAcceptedAfterWithdrawalDoesNotReopen(t *testing.T) {
	ctx := context.Background()
	rooms := store.New(testdb.Migrated(t).Pool)
	lifecycle := chat.NewLifecycle(rooms, 24*time.Hour, nil)
	errand := uuid.New()
	ensured, err := rooms.EnsureRoom(ctx, errand, requester, courierA)
	require.NoError(t, err)
	_, _, err = rooms.EndRoom(ctx, ensured.ID, chat.Ending{
		Reason: chat.EndWithdrawn, EndedAt: start, ClosesAt: start.Add(24 * time.Hour),
	})
	require.NoError(t, err)

	result, err := lifecycle.OnAccepted(ctx, accepted(errand, courierA, 2))
	require.NoError(t, err)
	require.False(t, result.Opened)
	require.Equal(t, ensured.ID, result.Room.ID)
	require.Equal(t, 2, *result.Room.OpenedVersion)
	require.NotNil(t, result.Room.Ending)
	require.Equal(t, chat.EndWithdrawn, result.Room.Ending.Reason)
}

// Courier A withdrew and courier B accepted: B gets a new, empty room.
func TestANewCourierGetsANewRoom(t *testing.T) {
	ctx := context.Background()
	rooms := store.New(testdb.Migrated(t).Pool)
	lifecycle := chat.NewLifecycle(rooms, 24*time.Hour, nil)
	errand := uuid.New()

	roomA, err := lifecycle.OnAccepted(ctx, accepted(errand, courierA, 2))
	require.NoError(t, err)
	roomB, err := lifecycle.OnAccepted(ctx, accepted(errand, courierB, 4))
	require.NoError(t, err)
	require.True(t, roomB.Opened)
	require.NotEqual(t, roomA.Room.ID, roomB.Room.ID)
}

func TestOnAcceptedRejectsInvalidEvents(t *testing.T) {
	// The checks run before any database call, so no database is needed.
	lifecycle := chat.NewLifecycle(unusedStore{}, 24*time.Hour, nil)
	valid := accepted(uuid.New(), courierA, 2)
	for name, change := range map[string]func(*chat.AcceptedEvent){
		"no errand":          func(e *chat.AcceptedEvent) { e.ErrandID = uuid.Nil },
		"version 0":          func(e *chat.AcceptedEvent) { e.ErrandVersion = 0 },
		"no requester":       func(e *chat.AcceptedEvent) { e.RequesterID = "" },
		"no courier":         func(e *chat.AcceptedEvent) { e.CourierID = "" },
		"requester delivers": func(e *chat.AcceptedEvent) { e.CourierID = e.RequesterID },
		"no occurrence time": func(e *chat.AcceptedEvent) { e.OccurredAt = time.Time{} },
	} {
		t.Run(name, func(t *testing.T) {
			event := valid
			change(&event)
			_, err := lifecycle.OnAccepted(context.Background(), event)
			require.ErrorIs(t, err, chat.ErrInvalidEvent)
		})
	}
}

type unusedStore struct{}

func (unusedStore) InTx(context.Context, func(chat.Tx) error) error {
	panic("the database must not be used for an invalid event")
}

func TestOnAcceptedAnnouncesOnlyNewRooms(t *testing.T) {
	ctx := context.Background()
	rooms := store.New(testdb.Migrated(t).Pool)
	notifier := &recordingNotifier{}
	lifecycle := chat.NewLifecycle(rooms, 24*time.Hour, notifier)
	errand := uuid.New()

	first, err := lifecycle.OnAccepted(ctx, accepted(errand, courierA, 2))
	require.NoError(t, err)
	require.Len(t, notifier.opened, 1)
	require.Equal(t, first.Room.ID, notifier.opened[0].ID)
	require.Empty(t, notifier.closing)

	_, err = lifecycle.OnAccepted(ctx, accepted(errand, courierA, 2))
	require.NoError(t, err)
	require.Len(t, notifier.opened, 1, "a repeated event announces nothing")

	// Completion already recorded: the new room is announced and is closing.
	other := uuid.New()
	require.NoError(t, rooms.RecordErrandEnding(ctx, chat.ErrandEnding{ErrandID: other, Reason: chat.EndRefunded, EndedAt: start}))
	_, err = lifecycle.OnAccepted(ctx, accepted(other, courierB, 2))
	require.NoError(t, err)
	require.Len(t, notifier.opened, 2)
	require.Len(t, notifier.closing, 1)
	require.Equal(t, chat.EndRefunded, notifier.closing[0].Ending.Reason)
}
