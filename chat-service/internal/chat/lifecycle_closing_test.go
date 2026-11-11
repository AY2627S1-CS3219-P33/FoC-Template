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
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/chat/chattest"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/store"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/testdb"
)

// Room closing (phase 7), against a real PostgreSQL like the other lifecycle
// tests: the earliest-ending rule lives in the SQL.

const closeDelay = 24 * time.Hour

func withdrawn(errandID uuid.UUID, courier string, at time.Time) chat.WithdrawnEvent {
	return chat.WithdrawnEvent{
		ErrandID: errandID, ErrandVersion: 3,
		RequesterID: requester, CourierID: courier, OccurredAt: at,
	}
}

func ended(errandID uuid.UUID, reason chat.EndReason, at time.Time) chat.ErrandEndedEvent {
	return chat.ErrandEndedEvent{ErrandID: errandID, ErrandVersion: 9, Reason: reason, OccurredAt: at}
}

func newLifecycle(t *testing.T) (*chat.Lifecycle, *store.Store, *recordingNotifier) {
	t.Helper()
	rooms := store.New(testdb.Migrated(t).Pool)
	notifier := &recordingNotifier{}
	return chat.NewLifecycle(rooms, closeDelay, notifier), rooms, notifier
}

func roomOf(t *testing.T, rooms *store.Store, errandID uuid.UUID, courier string) chat.Room {
	t.Helper()
	listed, err := rooms.ListRoomsForErrand(context.Background(), errandID)
	require.NoError(t, err)
	for _, room := range listed {
		if room.CourierID == courier {
			return room
		}
	}
	t.Fatalf("no room for courier %s", courier)
	return chat.Room{}
}

func requireEnding(t *testing.T, room chat.Room, reason chat.EndReason, at time.Time) {
	t.Helper()
	require.NotNil(t, room.Ending, "the room should have ended")
	require.Equal(t, reason, room.Ending.Reason)
	require.True(t, at.Equal(room.Ending.EndedAt), "ended at %s, want %s", room.Ending.EndedAt, at)
	require.True(t, at.Add(closeDelay).Equal(room.Ending.ClosesAt), "closes 24 hours after the end")
}

// The normal flow: accepted, then completed.
func TestCompletionEndsTheRoom(t *testing.T) {
	ctx := context.Background()
	lifecycle, rooms, notifier := newLifecycle(t)
	errand := uuid.New()
	_, err := lifecycle.OnAccepted(ctx, accepted(errand, courierA, 2))
	require.NoError(t, err)

	completedAt := start.Add(40 * time.Minute)
	result, err := lifecycle.OnErrandEnded(ctx, ended(errand, chat.EndCompleted, completedAt))
	require.NoError(t, err)
	require.Len(t, result.Closing, 1)
	requireEnding(t, result.Closing[0], chat.EndCompleted, completedAt)
	requireEnding(t, roomOf(t, rooms, errand, courierA), chat.EndCompleted, completedAt)
	require.Len(t, notifier.closing, 1, "room_closing is sent after the commit")

	again, err := lifecycle.OnErrandEnded(ctx, ended(errand, chat.EndCompleted, completedAt))
	require.NoError(t, err, "RabbitMQ may deliver an event twice")
	require.Empty(t, again.Closing, "the repeat changes nothing")
	require.Len(t, notifier.closing, 1, "and announces nothing")
}

func TestRefundEndsTheRoom(t *testing.T) {
	ctx := context.Background()
	lifecycle, rooms, _ := newLifecycle(t)
	errand := uuid.New()
	_, err := lifecycle.OnAccepted(ctx, accepted(errand, courierA, 2))
	require.NoError(t, err)

	refundedAt := start.Add(3 * time.Hour)
	result, err := lifecycle.OnErrandEnded(ctx, ended(errand, chat.EndRefunded, refundedAt))
	require.NoError(t, err)
	require.Len(t, result.Closing, 1)
	requireEnding(t, roomOf(t, rooms, errand, courierA), chat.EndRefunded, refundedAt)
}

func TestWithdrawalEndsOnlyThatCouriersRoom(t *testing.T) {
	ctx := context.Background()
	lifecycle, rooms, notifier := newLifecycle(t)
	errand := uuid.New()
	_, err := lifecycle.OnAccepted(ctx, accepted(errand, courierA, 2))
	require.NoError(t, err)

	withdrewAt := start.Add(10 * time.Minute)
	result, err := lifecycle.OnWithdrawn(ctx, withdrawn(errand, courierA, withdrewAt))
	require.NoError(t, err)
	require.True(t, result.Closing)
	requireEnding(t, result.Room, chat.EndWithdrawn, withdrewAt)
	require.Len(t, notifier.closing, 1)

	again, err := lifecycle.OnWithdrawn(ctx, withdrawn(errand, courierA, withdrewAt))
	require.NoError(t, err)
	require.False(t, again.Closing, "the repeat changes nothing")
	require.Len(t, notifier.closing, 1)

	// Courier B accepts next: a new, open room.
	roomB, err := lifecycle.OnAccepted(ctx, accepted(errand, courierB, 4))
	require.NoError(t, err)
	require.True(t, roomB.Opened)
	require.Nil(t, roomB.Room.Ending)
	requireEnding(t, roomOf(t, rooms, errand, courierA), chat.EndWithdrawn, withdrewAt)
}

// A withdrew, B accepted, then the errand completed: B's room ends at the
// completion, and A's keeps its earlier withdrawal ending.
func TestCompletionKeepsAnEarlierWithdrawal(t *testing.T) {
	ctx := context.Background()
	lifecycle, rooms, notifier := newLifecycle(t)
	errand := uuid.New()
	withdrewAt := start.Add(10 * time.Minute)
	completedAt := start.Add(2 * time.Hour)

	_, err := lifecycle.OnAccepted(ctx, accepted(errand, courierA, 2))
	require.NoError(t, err)
	_, err = lifecycle.OnWithdrawn(ctx, withdrawn(errand, courierA, withdrewAt))
	require.NoError(t, err)
	_, err = lifecycle.OnAccepted(ctx, accepted(errand, courierB, 4))
	require.NoError(t, err)

	result, err := lifecycle.OnErrandEnded(ctx, ended(errand, chat.EndCompleted, completedAt))
	require.NoError(t, err)
	require.Len(t, result.Closing, 1, "only B's room changed")
	require.Equal(t, courierB, result.Closing[0].CourierID)

	requireEnding(t, roomOf(t, rooms, errand, courierA), chat.EndWithdrawn, withdrewAt)
	requireEnding(t, roomOf(t, rooms, errand, courierB), chat.EndCompleted, completedAt)
	require.Len(t, notifier.closing, 2, "one for A's withdrawal, one for B's completion")
}

// errand.withdrawn processed before errand.accepted: the room is created
// already ending, and the late acceptance neither reopens nor re-announces it.
func TestWithdrawalBeforeAcceptance(t *testing.T) {
	ctx := context.Background()
	lifecycle, rooms, notifier := newLifecycle(t)
	errand := uuid.New()
	withdrewAt := start.Add(10 * time.Minute)

	result, err := lifecycle.OnWithdrawn(ctx, withdrawn(errand, courierA, withdrewAt))
	require.NoError(t, err)
	require.True(t, result.Closing)
	require.Nil(t, result.Room.OpenedVersion, "the acceptance has not arrived yet")

	late, err := lifecycle.OnAccepted(ctx, accepted(errand, courierA, 2))
	require.NoError(t, err)
	require.False(t, late.Opened)
	require.False(t, late.Closing)
	require.Empty(t, notifier.opened)
	room := roomOf(t, rooms, errand, courierA)
	require.Equal(t, 2, *room.OpenedVersion)
	requireEnding(t, room, chat.EndWithdrawn, withdrewAt)
}

// errand.completed processed before errand.accepted: no room exists yet, but
// the ending is remembered, so the late acceptance creates the room ending.
func TestCompletionBeforeAcceptance(t *testing.T) {
	ctx := context.Background()
	lifecycle, rooms, notifier := newLifecycle(t)
	errand := uuid.New()
	completedAt := start.Add(2 * time.Hour)

	result, err := lifecycle.OnErrandEnded(ctx, ended(errand, chat.EndCompleted, completedAt))
	require.NoError(t, err)
	require.Empty(t, result.Closing)

	late, err := lifecycle.OnAccepted(ctx, accepted(errand, courierA, 2))
	require.NoError(t, err)
	require.True(t, late.Opened)
	require.True(t, late.Closing)
	requireEnding(t, roomOf(t, rooms, errand, courierA), chat.EndCompleted, completedAt)
	require.Len(t, notifier.opened, 1)
	require.Len(t, notifier.closing, 1)
}

// A withdrawal that happened before the completion but is processed after
// it moves the ending earlier: the earliest ending always wins.
func TestALateEarlierWithdrawalMovesTheEndingEarlier(t *testing.T) {
	ctx := context.Background()
	lifecycle, rooms, notifier := newLifecycle(t)
	errand := uuid.New()
	withdrewAt := start.Add(10 * time.Minute)
	completedAt := start.Add(2 * time.Hour)

	_, err := lifecycle.OnAccepted(ctx, accepted(errand, courierA, 2))
	require.NoError(t, err)
	_, err = lifecycle.OnErrandEnded(ctx, ended(errand, chat.EndCompleted, completedAt))
	require.NoError(t, err)

	result, err := lifecycle.OnWithdrawn(ctx, withdrawn(errand, courierA, withdrewAt))
	require.NoError(t, err)
	require.True(t, result.Closing)
	requireEnding(t, roomOf(t, rooms, errand, courierA), chat.EndWithdrawn, withdrewAt)
	require.Len(t, notifier.closing, 2, "browsers learn the new, earlier closing time")

	// The reverse: a later ending never moves it back.
	again, err := lifecycle.OnErrandEnded(ctx, ended(errand, chat.EndCompleted, completedAt))
	require.NoError(t, err)
	require.Empty(t, again.Closing)
}

// The whole rule end to end with a real event: still writable while
// CLOSING, refused one second after closesAt, and readable afterwards.
func TestAnEndedRoomClosesAfterTheDelay(t *testing.T) {
	ctx := context.Background()
	lifecycle, rooms, _ := newLifecycle(t)
	clock := chattest.NewClock(start)
	service := chat.NewService(rooms, chat.Options{Clock: clock.Now, MaxMessageLength: 2000})
	errand := uuid.New()

	opened, err := lifecycle.OnAccepted(ctx, accepted(errand, courierA, 2))
	require.NoError(t, err)
	roomID := opened.Room.ID
	_, err = lifecycle.OnErrandEnded(ctx, ended(errand, chat.EndCompleted, start))
	require.NoError(t, err)

	clock.Set(start.Add(time.Hour))
	view, err := service.Room(ctx, requester, roomID)
	require.NoError(t, err)
	require.Equal(t, chat.StatusClosing, view.Status)
	_, _, err = service.Send(ctx, courierA, roomID, "c1", "Left it with the guard")
	require.NoError(t, err, "a CLOSING room is still writable")

	clock.Set(start.Add(closeDelay + time.Second))
	view, err = service.Room(ctx, requester, roomID)
	require.NoError(t, err)
	require.Equal(t, chat.StatusClosed, view.Status)
	_, _, err = service.Send(ctx, requester, roomID, "c2", "Thanks!")
	require.ErrorIs(t, err, chat.ErrRoomClosed)

	history, err := service.History(ctx, requester, roomID, chat.Page{})
	require.NoError(t, err, "a CLOSED room is still readable")
	require.Len(t, history, 1)
}

func TestClosingEventsRejectInvalidInput(t *testing.T) {
	// The checks run before any database call, so no database is needed.
	lifecycle := chat.NewLifecycle(unusedStore{}, closeDelay, nil)
	ctx := context.Background()

	validWithdrawal := withdrawn(uuid.New(), courierA, start)
	for name, change := range map[string]func(*chat.WithdrawnEvent){
		"no errand":           func(e *chat.WithdrawnEvent) { e.ErrandID = uuid.Nil },
		"version 0":           func(e *chat.WithdrawnEvent) { e.ErrandVersion = 0 },
		"no courier":          func(e *chat.WithdrawnEvent) { e.CourierID = "" },
		"no requester":        func(e *chat.WithdrawnEvent) { e.RequesterID = "" },
		"requester withdraws": func(e *chat.WithdrawnEvent) { e.CourierID = e.RequesterID },
		"no occurrence time":  func(e *chat.WithdrawnEvent) { e.OccurredAt = time.Time{} },
	} {
		t.Run("withdrawn/"+name, func(t *testing.T) {
			event := validWithdrawal
			change(&event)
			_, err := lifecycle.OnWithdrawn(ctx, event)
			require.ErrorIs(t, err, chat.ErrInvalidEvent)
		})
	}

	validEnd := ended(uuid.New(), chat.EndCompleted, start)
	for name, change := range map[string]func(*chat.ErrandEndedEvent){
		"no errand":          func(e *chat.ErrandEndedEvent) { e.ErrandID = uuid.Nil },
		"version 0":          func(e *chat.ErrandEndedEvent) { e.ErrandVersion = 0 },
		"withdrawn reason":   func(e *chat.ErrandEndedEvent) { e.Reason = chat.EndWithdrawn },
		"no reason":          func(e *chat.ErrandEndedEvent) { e.Reason = "" },
		"no occurrence time": func(e *chat.ErrandEndedEvent) { e.OccurredAt = time.Time{} },
	} {
		t.Run("ended/"+name, func(t *testing.T) {
			event := validEnd
			change(&event)
			_, err := lifecycle.OnErrandEnded(ctx, event)
			require.ErrorIs(t, err, chat.ErrInvalidEvent)
		})
	}
}
