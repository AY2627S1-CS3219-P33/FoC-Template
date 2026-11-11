// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-10
// Scope: Implementation of test code (unit and integration tests).
// Author review: Reviewed, validated, and tested by the authors.

package chat_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/chat"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/chat/chattest"
)

const (
	requester = "auth0|requester"
	courierA  = "auth0|courier-a"
	courierB  = "auth0|courier-b"
	outsider  = "auth0|outsider"
)

var start = time.Date(2026, 11, 9, 14, 0, 0, 0, time.UTC)

func newService(t *testing.T) (*chat.Service, *chattest.Store, *chattest.Clock) {
	t.Helper()
	store := &chattest.Store{}
	clock := chattest.NewClock(start)
	return chat.NewService(store, chat.Options{Clock: clock.Now, MaxMessageLength: 2000}), store, clock
}

func ptr(v int64) *int64 { return &v }

func TestParticipantsSendAndRead(t *testing.T) {
	ctx := context.Background()
	service, store, _ := newService(t)
	room := store.AddRoom(uuid.New(), requester, courierA)

	sent, created, err := service.Send(ctx, requester, room.ID, "c1", "I'm at the lobby")
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, chat.RoleRequester, sent.SenderRole)
	require.Equal(t, requester, sent.SenderID)

	reply, _, err := service.Send(ctx, courierA, room.ID, "c2", "On my way up")
	require.NoError(t, err)
	require.Equal(t, chat.RoleCourier, reply.SenderRole)

	for _, reader := range []string{requester, courierA} {
		history, err := service.History(ctx, reader, room.ID, chat.Page{})
		require.NoError(t, err)
		require.Len(t, history, 2)
		require.Equal(t, "I'm at the lobby", history[0].Body)
		require.Equal(t, chat.RoleRequester, history[0].SenderRole)
		require.Equal(t, chat.RoleCourier, history[1].SenderRole)
	}
}

func TestSendRetryIsIdempotent(t *testing.T) {
	ctx := context.Background()
	service, store, _ := newService(t)
	room := store.AddRoom(uuid.New(), requester, courierA)

	first, created, err := service.Send(ctx, requester, room.ID, "c1", "hello")
	require.NoError(t, err)
	require.True(t, created)
	retry, created, err := service.Send(ctx, requester, room.ID, "c1", "hello")
	require.NoError(t, err)
	require.False(t, created, "a retried send stores nothing new")
	require.Equal(t, first.ID, retry.ID)
	require.Len(t, store.Messages(), 1)

	_, _, err = service.Send(ctx, courierA, room.ID, "c1", "hi")
	require.ErrorIs(t, err, chat.ErrInvalidMessage, "the other participant's clientMsgId is not handed back")
}

// Plan, section 7: sending works while CLOSING and is refused one second
// after closesAt; reading works while CLOSED.
func TestClosingAndClosedRooms(t *testing.T) {
	ctx := context.Background()
	service, store, clock := newService(t)
	room := store.AddRoom(uuid.New(), requester, courierA)
	_, _, err := service.Send(ctx, requester, room.ID, "c1", "before the end")
	require.NoError(t, err)

	endedAt := start.Add(time.Hour)
	closesAt := endedAt.Add(24 * time.Hour)
	store.EndRoom(room.ID, chat.EndCompleted, endedAt, 24*time.Hour)

	clock.Set(closesAt.Add(-time.Second))
	view, err := service.Room(ctx, requester, room.ID)
	require.NoError(t, err)
	require.Equal(t, chat.StatusClosing, view.Status)
	_, _, err = service.Send(ctx, courierA, room.ID, "c2", "left it with the guard")
	require.NoError(t, err, "sending still works while CLOSING")

	clock.Set(closesAt.Add(time.Second))
	view, err = service.Room(ctx, requester, room.ID)
	require.NoError(t, err)
	require.Equal(t, chat.StatusClosed, view.Status)
	_, _, err = service.Send(ctx, requester, room.ID, "c3", "too late")
	require.ErrorIs(t, err, chat.ErrRoomClosed)

	history, err := service.History(ctx, courierA, room.ID, chat.Page{})
	require.NoError(t, err, "reading works while CLOSED")
	require.Len(t, history, 2)
}

// Plan, section 7: courier B cannot read courier A's room for the same
// errand, and the requester of errand X cannot read errand Y's room.
func TestOutsidersGetRoomNotFound(t *testing.T) {
	ctx := context.Background()
	service, store, _ := newService(t)
	errandX := uuid.New()
	roomA := store.AddRoom(errandX, requester, courierA)
	store.AddRoom(errandX, requester, courierB)
	roomY := store.AddRoom(uuid.New(), "auth0|someone-else", courierB)

	cases := []struct {
		name   string
		userID string
		roomID uuid.UUID
	}{
		{"courier B reading courier A's room", courierB, roomA.ID},
		{"requester of X reading errand Y's room", requester, roomY.ID},
		{"stranger", outsider, roomA.ID},
		{"no user", "", roomA.ID},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := service.History(ctx, tt.userID, tt.roomID, chat.Page{})
			require.ErrorIs(t, err, chat.ErrRoomNotFound)
			_, _, err = service.Send(ctx, tt.userID, tt.roomID, "c1", "hi")
			require.ErrorIs(t, err, chat.ErrRoomNotFound)
			_, err = service.Room(ctx, tt.userID, tt.roomID)
			require.ErrorIs(t, err, chat.ErrRoomNotFound)
		})
	}

	_, err := service.History(ctx, requester, uuid.New(), chat.Page{})
	require.ErrorIs(t, err, chat.ErrRoomNotFound, "a room that does not exist looks the same")
	require.Empty(t, store.Messages(), "nothing was stored")
}

func TestRoomListsShowOnlyYourRoomsWithStatusAndRole(t *testing.T) {
	ctx := context.Background()
	service, store, clock := newService(t)
	errand := uuid.New()
	roomA := store.AddRoom(errand, requester, courierA)
	store.EndRoom(roomA.ID, chat.EndWithdrawn, start, time.Hour)
	roomB := store.AddRoom(errand, requester, courierB)
	_, _, err := service.Send(ctx, courierB, roomB.ID, "c1", "accepted!")
	require.NoError(t, err)
	clock.Set(start.Add(2 * time.Hour))

	forRequester, err := service.RoomsForErrand(ctx, requester, errand)
	require.NoError(t, err)
	require.Len(t, forRequester, 2)
	require.Equal(t, roomB.ID, forRequester[0].ID, "newest first")
	require.Equal(t, chat.StatusOpen, forRequester[0].Status)
	require.Equal(t, chat.StatusClosed, forRequester[1].Status, "A withdrew and the hour has passed")
	require.Equal(t, chat.RoleRequester, forRequester[0].MyRole)

	forB, err := service.RoomsForErrand(ctx, courierB, errand)
	require.NoError(t, err)
	require.Len(t, forB, 1)
	require.Equal(t, chat.RoleCourier, forB[0].MyRole)

	forOutsider, err := service.RoomsForErrand(ctx, outsider, errand)
	require.NoError(t, err)
	require.Empty(t, forOutsider)
	require.NotNil(t, forOutsider)

	mine, err := service.MyRooms(ctx, requester)
	require.NoError(t, err)
	require.Len(t, mine, 2)
	require.Equal(t, "accepted!", mine[0].LastMessage.Body)
	require.Nil(t, mine[1].LastMessage)

	none, err := service.MyRooms(ctx, outsider)
	require.NoError(t, err)
	require.Empty(t, none)
	require.NotNil(t, none)
}

func TestSendValidatesTheMessage(t *testing.T) {
	ctx := context.Background()
	service, store, _ := newService(t)
	room := store.AddRoom(uuid.New(), requester, courierA)

	invalid := []struct {
		name, clientMsgID, body string
	}{
		{"empty body", "c1", ""},
		{"only spaces", "c1", "  \n\t "},
		{"too long", "c1", strings.Repeat("a", 2001)},
		{"control character", "c1", "bell\a"},
		{"NUL", "c1", "a\x00b"},
		{"invalid UTF-8", "c1", "\xff\xfe"},
		{"empty clientMsgId", "", "hi"},
		{"clientMsgId too long", strings.Repeat("x", 65), "hi"},
		{"clientMsgId with spaces", "c 1", "hi"},
		{"clientMsgId with HTML", "<b>", "hi"},
	}
	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := service.Send(ctx, requester, room.ID, tt.clientMsgID, tt.body)
			require.ErrorIs(t, err, chat.ErrInvalidMessage)
		})
	}
	require.Empty(t, store.Messages())

	sent, _, err := service.Send(ctx, requester, room.ID, "6f1c2a52-8d0e-4b8a-9f5e-0d3c1b7a2e44", "  two\r\nlines\twith tab <b>kept</b>  ")
	require.NoError(t, err)
	require.Equal(t, "two\nlines\twith tab <b>kept</b>", sent.Body, "trimmed and stored as typed")

	_, _, err = service.Send(ctx, requester, room.ID, "c-max", strings.Repeat("é", 2000))
	require.NoError(t, err, "the limit counts characters, not bytes")
}

func TestMaxMessageLengthIsConfigurable(t *testing.T) {
	store := &chattest.Store{}
	room := store.AddRoom(uuid.New(), requester, courierA)
	service := chat.NewService(store, chat.Options{MaxMessageLength: 5})
	_, _, err := service.Send(context.Background(), requester, room.ID, "c1", "123456")
	require.ErrorIs(t, err, chat.ErrInvalidMessage)
	_, _, err = service.Send(context.Background(), requester, room.ID, "c1", "12345")
	require.NoError(t, err)
}

func TestHistoryPaging(t *testing.T) {
	ctx := context.Background()
	service, store, _ := newService(t)
	room := store.AddRoom(uuid.New(), requester, courierA)
	for _, body := range []string{"m1", "m2", "m3", "m4", "m5"} {
		_, _, err := service.Send(ctx, requester, room.ID, "c-"+body, body)
		require.NoError(t, err)
	}
	bodies := func(page chat.Page) []string {
		t.Helper()
		messages, err := service.History(ctx, courierA, room.ID, page)
		require.NoError(t, err)
		out := make([]string, len(messages))
		for i, m := range messages {
			out[i] = m.Body
		}
		return out
	}

	require.Equal(t, []string{"m4", "m5"}, bodies(chat.Page{Limit: 2}), "no cursor: the newest page")
	require.Equal(t, []string{"m3", "m4", "m5"}, bodies(chat.Page{After: ptr(2)}))
	require.Equal(t, []string{"m2", "m3"}, bodies(chat.Page{Before: ptr(4), Limit: 2}))
	require.Equal(t, []string{"m1", "m2", "m3", "m4", "m5"}, bodies(chat.Page{After: ptr(0)}))

	for _, page := range []chat.Page{
		{After: ptr(1), Before: ptr(3)},
		{After: ptr(-1)},
		{Before: ptr(0)},
		{Limit: -1},
		{Limit: chat.MaxPageSize + 1},
	} {
		_, err := service.History(ctx, courierA, room.ID, page)
		require.ErrorIs(t, err, chat.ErrInvalidPage)
	}
}

func TestStoreErrorsPassThrough(t *testing.T) {
	ctx := context.Background()
	service, store, _ := newService(t)
	room := store.AddRoom(uuid.New(), requester, courierA)
	store.FailWith(errors.Join(chat.ErrUnavailable, errors.New("database down")))

	_, err := service.MyRooms(ctx, requester)
	require.ErrorIs(t, err, chat.ErrUnavailable)
	_, _, err = service.Send(ctx, requester, room.ID, "c1", "hi")
	require.ErrorIs(t, err, chat.ErrUnavailable)
	_, err = service.History(ctx, requester, room.ID, chat.Page{})
	require.ErrorIs(t, err, chat.ErrUnavailable)
}

// recordingNotifier records what chat asked to push.
type recordingNotifier struct {
	sent    []chat.MessageView
	opened  []chat.Room
	closing []chat.Room
}

func (r *recordingNotifier) MessageSent(_ context.Context, _ chat.Room, m chat.MessageView) {
	r.sent = append(r.sent, m)
}
func (r *recordingNotifier) RoomOpened(_ context.Context, room chat.Room) {
	r.opened = append(r.opened, room)
}
func (r *recordingNotifier) RoomClosing(_ context.Context, room chat.Room) {
	r.closing = append(r.closing, room)
}

func TestSendPushesOnlyNewSavedMessages(t *testing.T) {
	ctx := context.Background()
	store := &chattest.Store{}
	clock := chattest.NewClock(start)
	notifier := &recordingNotifier{}
	service := chat.NewService(store, chat.Options{Clock: clock.Now, Notifier: notifier})
	room := store.AddRoom(uuid.New(), requester, courierA)

	_, _, err := service.Send(ctx, requester, room.ID, "c1", "hello")
	require.NoError(t, err)
	require.Len(t, notifier.sent, 1)
	require.Equal(t, "hello", notifier.sent[0].Body)
	require.Equal(t, chat.RoleRequester, notifier.sent[0].SenderRole)

	_, _, err = service.Send(ctx, requester, room.ID, "c1", "hello")
	require.NoError(t, err)
	require.Len(t, notifier.sent, 1, "a retry pushes nothing")

	_, _, err = service.Send(ctx, outsider, room.ID, "c2", "hi")
	require.ErrorIs(t, err, chat.ErrRoomNotFound)
	_, _, err = service.Send(ctx, requester, room.ID, "c3", "")
	require.ErrorIs(t, err, chat.ErrInvalidMessage)
	store.FailWith(errors.Join(chat.ErrUnavailable, errors.New("down")))
	_, _, err = service.Send(ctx, requester, room.ID, "c4", "lost")
	require.ErrorIs(t, err, chat.ErrUnavailable)
	require.Len(t, notifier.sent, 1, "nothing is pushed unless it was saved")
}
