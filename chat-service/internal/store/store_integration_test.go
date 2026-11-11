// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-10
// Scope: Implementation of test code (unit and integration tests).
// Author review: Reviewed, validated, and tested by the authors.

package store

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/chat"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/testdb"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/migrations"
)

const (
	requester = "auth0|requester"
	courierA  = "auth0|courier-a"
	courierB  = "auth0|courier-b"
	outsider  = "auth0|outsider"
)

// base is a fixed time; PostgreSQL keeps microseconds, so tests use whole
// seconds.
var base = time.Date(2026, 11, 9, 14, 0, 0, 0, time.UTC)

func newStore(t *testing.T) *Store {
	t.Helper()
	return New(testdb.Migrated(t).Pool)
}

func ending(reason chat.EndReason, endedAt time.Time) chat.Ending {
	return chat.Ending{Reason: reason, EndedAt: endedAt, ClosesAt: endedAt.Add(24 * time.Hour)}
}

func accept(t *testing.T, s *Store, errandID uuid.UUID, courier string) chat.Room {
	t.Helper()
	room, inserted, err := s.UpsertRoomOnAccept(context.Background(), chat.AcceptedRoom{
		ErrandID: errandID, RequesterID: requester, CourierID: courier, OpenedVersion: 3,
	})
	require.NoError(t, err)
	require.True(t, inserted)
	return room
}

func send(t *testing.T, s *Store, roomID uuid.UUID, clientMsgID, body string) chat.Message {
	t.Helper()
	message, inserted, err := s.InsertMessage(context.Background(), chat.NewMessage{
		RoomID: roomID, SenderID: requester, Body: body, ClientMsgID: clientMsgID,
	})
	require.NoError(t, err)
	require.True(t, inserted)
	return message
}

func requireEnding(t *testing.T, want chat.Ending, got *chat.Ending) {
	t.Helper()
	require.NotNil(t, got)
	require.Equal(t, want.Reason, got.Reason)
	require.True(t, want.EndedAt.Equal(got.EndedAt), "endedAt: want %s, got %s", want.EndedAt, got.EndedAt)
	require.True(t, want.ClosesAt.Equal(got.ClosesAt), "closesAt: want %s, got %s", want.ClosesAt, got.ClosesAt)
}

func bodies(messages []chat.Message) []string {
	out := make([]string, len(messages))
	for i, m := range messages {
		out[i] = m.Body
	}
	return out
}

func TestUpsertRoomOnAcceptIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	errand := uuid.New()

	room := accept(t, s, errand, courierA)
	require.Equal(t, errand, room.ErrandID)
	require.Equal(t, requester, room.RequesterID)
	require.Equal(t, courierA, room.CourierID)
	require.Equal(t, 3, *room.OpenedVersion)
	require.Nil(t, room.Ending)
	require.Equal(t, chat.StatusOpen, room.Status(time.Now()))

	again, inserted, err := s.UpsertRoomOnAccept(ctx, chat.AcceptedRoom{
		ErrandID: errand, RequesterID: requester, CourierID: courierA, OpenedVersion: 3,
	})
	require.NoError(t, err)
	require.False(t, inserted, "a repeated errand.accepted creates nothing")
	require.Equal(t, room.ID, again.ID)

	other := accept(t, s, errand, courierB)
	require.NotEqual(t, room.ID, other.ID, "a new courier gets a new room")
}

func TestUpsertRoomOnAcceptRejectsTheRequesterAsCourier(t *testing.T) {
	_, _, err := newStore(t).UpsertRoomOnAccept(context.Background(), chat.AcceptedRoom{
		ErrandID: uuid.New(), RequesterID: requester, CourierID: requester, OpenedVersion: 1,
	})
	require.ErrorIs(t, err, chat.ErrInvalidRoom)
	require.NotErrorIs(t, err, chat.ErrUnavailable, "retrying cannot fix it")
}

// errand.withdrawn arrives before errand.accepted: the late accept fills in
// the version but cannot reopen the room.
func TestWithdrawnBeforeAcceptedStaysEnded(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	errand := uuid.New()

	ensured, err := s.EnsureRoom(ctx, errand, requester, courierA)
	require.NoError(t, err)
	require.Nil(t, ensured.OpenedVersion)
	withdrawn := ending(chat.EndWithdrawn, base)
	_, changed, err := s.EndRoom(ctx, ensured.ID, withdrawn)
	require.NoError(t, err)
	require.True(t, changed)

	room, inserted, err := s.UpsertRoomOnAccept(ctx, chat.AcceptedRoom{
		ErrandID: errand, RequesterID: requester, CourierID: courierA, OpenedVersion: 4,
	})
	require.NoError(t, err)
	require.False(t, inserted)
	require.Equal(t, ensured.ID, room.ID)
	require.Equal(t, 4, *room.OpenedVersion)
	requireEnding(t, withdrawn, room.Ending)

	again, err := s.EnsureRoom(ctx, errand, requester, courierA)
	require.NoError(t, err)
	require.Equal(t, room.ID, again.ID, "EnsureRoom returns the existing room")
	require.Equal(t, 4, *again.OpenedVersion)
}

func TestEndRoomKeepsTheEarliestEnding(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	room := accept(t, s, uuid.New(), courierA)

	first := ending(chat.EndWithdrawn, base)
	ended, changed, err := s.EndRoom(ctx, room.ID, first)
	require.NoError(t, err)
	require.True(t, changed)
	requireEnding(t, first, ended.Ending)

	for name, e := range map[string]chat.Ending{
		"same event again": first,
		"a later event":    ending(chat.EndCompleted, base.Add(time.Hour)),
	} {
		got, changed, err := s.EndRoom(ctx, room.ID, e)
		require.NoError(t, err, name)
		require.False(t, changed, name)
		requireEnding(t, first, got.Ending)
	}

	earlier := ending(chat.EndCompleted, base.Add(-time.Minute))
	got, changed, err := s.EndRoom(ctx, room.ID, earlier)
	require.NoError(t, err)
	require.True(t, changed, "an earlier ending moves the end time earlier")
	requireEnding(t, earlier, got.Ending)

	_, _, err = s.EndRoom(ctx, uuid.New(), first)
	require.ErrorIs(t, err, chat.ErrRoomNotFound)
}

func TestEndRoomRejectsAClosingTimeBeforeTheEnd(t *testing.T) {
	s := newStore(t)
	room := accept(t, s, uuid.New(), courierA)
	_, _, err := s.EndRoom(context.Background(), room.ID, chat.Ending{
		Reason: chat.EndCompleted, EndedAt: base, ClosesAt: base.Add(-time.Second),
	})
	require.ErrorIs(t, err, chat.ErrInvalidRoom)
}

// Courier A withdraws, courier B accepts, then the errand completes: A's
// room keeps its earlier withdrawal time.
func TestEndRoomsForErrandKeepsEarlierWithdrawals(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	errand := uuid.New()
	roomA := accept(t, s, errand, courierA)
	withdrawn := ending(chat.EndWithdrawn, base)
	_, _, err := s.EndRoom(ctx, roomA.ID, withdrawn)
	require.NoError(t, err)
	roomB := accept(t, s, errand, courierB)
	unrelated := accept(t, s, uuid.New(), courierA)

	completed := ending(chat.EndCompleted, base.Add(2*time.Hour))
	changed, err := s.EndRoomsForErrand(ctx, errand, completed)
	require.NoError(t, err)
	require.Len(t, changed, 1)
	require.Equal(t, roomB.ID, changed[0].ID)
	requireEnding(t, completed, changed[0].Ending)

	gotA, err := s.GetRoom(ctx, roomA.ID)
	require.NoError(t, err)
	requireEnding(t, withdrawn, gotA.Ending)
	gotOther, err := s.GetRoom(ctx, unrelated.ID)
	require.NoError(t, err)
	require.Nil(t, gotOther.Ending, "other errands are untouched")

	again, err := s.EndRoomsForErrand(ctx, errand, completed)
	require.NoError(t, err)
	require.Empty(t, again, "a repeated event changes nothing")
}

func TestErrandEndingKeepsTheEarliest(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	errand := uuid.New()

	none, err := s.ErrandEnding(ctx, errand)
	require.NoError(t, err)
	require.Nil(t, none)

	completed := chat.ErrandEnding{ErrandID: errand, Reason: chat.EndCompleted, EndedAt: base}
	require.NoError(t, s.RecordErrandEnding(ctx, completed))
	require.NoError(t, s.RecordErrandEnding(ctx, completed), "repeats are harmless")
	require.NoError(t, s.RecordErrandEnding(ctx, chat.ErrandEnding{ErrandID: errand, Reason: chat.EndRefunded, EndedAt: base.Add(time.Hour)}))

	got, err := s.ErrandEnding(ctx, errand)
	require.NoError(t, err)
	require.Equal(t, chat.EndCompleted, got.Reason)
	require.True(t, base.Equal(got.EndedAt))

	require.ErrorIs(t, s.RecordErrandEnding(ctx, chat.ErrandEnding{ErrandID: errand, Reason: chat.EndWithdrawn, EndedAt: base}), chat.ErrInvalidRoom)
}

func TestGetRoomNotFound(t *testing.T) {
	_, err := newStore(t).GetRoom(context.Background(), uuid.New())
	require.ErrorIs(t, err, chat.ErrRoomNotFound)
}

func TestListRoomsForErrandAndUserShowsOnlyYourRooms(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	errand := uuid.New()
	roomA := accept(t, s, errand, courierA)
	roomB := accept(t, s, errand, courierB)
	accept(t, s, uuid.New(), courierA) // another errand

	forRequester, err := s.ListRoomsForErrandAndUser(ctx, errand, requester)
	require.NoError(t, err)
	require.Len(t, forRequester, 2, "one room per courier who accepted")
	require.Equal(t, roomB.ID, forRequester[0].ID, "newest first")
	require.Equal(t, roomA.ID, forRequester[1].ID)

	forB, err := s.ListRoomsForErrandAndUser(ctx, errand, courierB)
	require.NoError(t, err)
	require.Len(t, forB, 1, "courier B never sees courier A's room")
	require.Equal(t, roomB.ID, forB[0].ID)

	forOutsider, err := s.ListRoomsForErrandAndUser(ctx, errand, outsider)
	require.NoError(t, err)
	require.Empty(t, forOutsider)
	require.NotNil(t, forOutsider, "empty list, not nil, so JSON shows []")
}

func TestListRoomsForUserIncludesTheLastMessage(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	older := accept(t, s, uuid.New(), courierA)
	newer := accept(t, s, uuid.New(), courierB)
	send(t, s, older.ID, "c1", "first")
	send(t, s, older.ID, "c2", "second")

	rooms, err := s.ListRoomsForUser(ctx, requester, 50)
	require.NoError(t, err)
	require.Len(t, rooms, 2)
	require.Equal(t, newer.ID, rooms[0].Room.ID, "newest first")
	require.Nil(t, rooms[0].LastMessage, "no messages yet")
	require.Equal(t, older.ID, rooms[1].Room.ID)
	require.Equal(t, "second", rooms[1].LastMessage.Body)

	forA, err := s.ListRoomsForUser(ctx, courierA, 50)
	require.NoError(t, err)
	require.Len(t, forA, 1)
	require.Equal(t, older.ID, forA[0].Room.ID)

	limited, err := s.ListRoomsForUser(ctx, requester, 1)
	require.NoError(t, err)
	require.Len(t, limited, 1)

	none, err := s.ListRoomsForUser(ctx, outsider, 50)
	require.NoError(t, err)
	require.Empty(t, none)

	_, err = s.ListRoomsForUser(ctx, requester, 0)
	require.Error(t, err)
}

func TestListRoomsForErrandShowsEveryRoom(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	errand := uuid.New()
	roomA := accept(t, s, errand, courierA)
	_, _, err := s.EndRoom(ctx, roomA.ID, ending(chat.EndWithdrawn, base))
	require.NoError(t, err)
	roomB := accept(t, s, errand, courierB)

	rooms, err := s.ListRoomsForErrand(ctx, errand)
	require.NoError(t, err)
	require.Len(t, rooms, 2, "including the withdrawn courier's room")
	require.Equal(t, roomA.ID, rooms[0].ID, "oldest first")
	require.Equal(t, roomB.ID, rooms[1].ID)
}

func TestInsertMessageIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	room := accept(t, s, uuid.New(), courierA)

	first := send(t, s, room.ID, "client-1", "I'm at the lobby")
	require.Equal(t, room.ID, first.RoomID)
	require.Equal(t, requester, first.SenderID)
	require.Equal(t, "client-1", first.ClientMsgID)
	require.False(t, first.CreatedAt.IsZero())

	retry, inserted, err := s.InsertMessage(ctx, chat.NewMessage{
		RoomID: room.ID, SenderID: requester, Body: "I'm at the lobby (retry)", ClientMsgID: "client-1",
	})
	require.NoError(t, err)
	require.False(t, inserted, "a retried send stores nothing new")
	require.Equal(t, first.ID, retry.ID)
	require.Equal(t, "I'm at the lobby", retry.Body, "the stored message wins")

	all, err := s.ListMessagesAfter(ctx, room.ID, 0, MaxRowLimit)
	require.NoError(t, err)
	require.Len(t, all, 1)
}

func TestInsertMessageErrors(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	room := accept(t, s, uuid.New(), courierA)

	_, _, err := s.InsertMessage(ctx, chat.NewMessage{RoomID: uuid.New(), SenderID: requester, Body: "hi", ClientMsgID: "c1"})
	require.ErrorIs(t, err, chat.ErrRoomNotFound)

	for _, m := range []chat.NewMessage{
		{RoomID: room.ID, SenderID: requester, Body: "", ClientMsgID: "c1"},
		{RoomID: room.ID, SenderID: requester, Body: strings.Repeat("a", 2001), ClientMsgID: "c1"},
		{RoomID: room.ID, SenderID: requester, Body: "hi", ClientMsgID: strings.Repeat("x", 65)},
	} {
		_, _, err := s.InsertMessage(ctx, m)
		require.ErrorIs(t, err, chat.ErrInvalidMessage)
		require.NotContains(t, err.Error(), "aaaa", "errors never carry message text")
	}
}

func TestMessagePaging(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	room := accept(t, s, uuid.New(), courierA)
	other := accept(t, s, uuid.New(), courierB)
	var messages []chat.Message
	for _, body := range []string{"m1", "m2", "m3", "m4", "m5"} {
		messages = append(messages, send(t, s, room.ID, "c-"+body, body))
	}
	send(t, s, other.ID, "c-x", "other room")

	after, err := s.ListMessagesAfter(ctx, room.ID, 0, 2)
	require.NoError(t, err)
	require.Equal(t, []string{"m1", "m2"}, bodies(after), "catch-up from the start, oldest first")

	after, err = s.ListMessagesAfter(ctx, room.ID, messages[1].ID, 50)
	require.NoError(t, err)
	require.Equal(t, []string{"m3", "m4", "m5"}, bodies(after))

	after, err = s.ListMessagesAfter(ctx, room.ID, messages[4].ID, 50)
	require.NoError(t, err)
	require.Empty(t, after, "nothing newer")

	latest, err := s.ListMessagesBefore(ctx, room.ID, math.MaxInt64, 2)
	require.NoError(t, err)
	require.Equal(t, []string{"m4", "m5"}, bodies(latest), "newest page, oldest first")

	before, err := s.ListMessagesBefore(ctx, room.ID, messages[3].ID, 2)
	require.NoError(t, err)
	require.Equal(t, []string{"m2", "m3"}, bodies(before), "the two just before m4")

	all, err := s.ListMessagesAfter(ctx, room.ID, 0, MaxRowLimit)
	require.NoError(t, err)
	require.Equal(t, []string{"m1", "m2", "m3", "m4", "m5"}, bodies(all), "only this room's messages")

	_, err = s.ListMessagesAfter(ctx, room.ID, 0, MaxRowLimit+1)
	require.Error(t, err)
}

func TestWithTxRollsBackOnError(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	errand := uuid.New()
	boom := errors.New("boom")

	err := s.WithTx(ctx, func(q *Queries) error {
		_, _, err := q.UpsertRoomOnAccept(ctx, chat.AcceptedRoom{ErrandID: errand, RequesterID: requester, CourierID: courierA, OpenedVersion: 1})
		require.NoError(t, err)
		return boom
	})
	require.ErrorIs(t, err, boom)

	rooms, err := s.ListRoomsForErrand(ctx, errand)
	require.NoError(t, err)
	require.Empty(t, rooms, "nothing is saved when the transaction fails")
}

func TestLockErrandMakesASecondTransactionWait(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	errand := uuid.New()
	locked := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)

	go func() {
		firstDone <- s.WithTx(ctx, func(q *Queries) error {
			if err := q.LockErrand(ctx, errand); err != nil {
				return err
			}
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked

	// While the first holds the lock, a second waits and gives up at its deadline.
	waitCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	err := s.WithTx(waitCtx, func(q *Queries) error { return q.LockErrand(waitCtx, errand) })
	require.ErrorIs(t, err, chat.ErrUnavailable)

	// Another errand is not blocked.
	require.NoError(t, s.WithTx(ctx, func(q *Queries) error { return q.LockErrand(ctx, uuid.New()) }))

	close(release)
	require.NoError(t, <-firstDone)
	require.NoError(t, s.WithTx(ctx, func(q *Queries) error { return q.LockErrand(ctx, errand) }),
		"the lock is free once the first transaction ends")
}

func TestSchemaCheck(t *testing.T) {
	ctx := context.Background()
	latest, err := migrations.LatestVersion()
	require.NoError(t, err)

	migrated := testdb.Migrated(t)
	require.NoError(t, SchemaCheck(migrated.Pool, latest)(ctx))
	require.EqualError(t, SchemaCheck(migrated.Pool, latest+1)(ctx), "database schema is behind this build")

	empty := testdb.Empty(t)
	require.EqualError(t, SchemaCheck(empty.Pool, latest)(ctx), "schema version is unavailable", "never migrated")
}
