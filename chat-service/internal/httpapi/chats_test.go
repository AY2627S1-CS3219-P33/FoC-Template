// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-10
// Scope: Implementation of test code (unit and integration tests).
// Author review: Reviewed, validated, and tested by the authors.

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/chat"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/chat/chattest"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/protocol"
)

const (
	requester = "auth0|requester"
	courier   = "auth0|courier"
	outsider  = "auth0|outsider"
)

var start = time.Date(2026, 11, 9, 14, 0, 0, 0, time.UTC)

// fakePort accepts the tokens in its map and nothing else, so the handler
// tests need neither Auth0 nor signed tokens. auth's own tests cover real
// token verification.
type fakePort map[string]auth.Principal

func (p fakePort) Authenticate(_ context.Context, token string) (auth.Principal, error) {
	principal, ok := p[token]
	if !ok {
		return auth.Principal{}, &auth.AuthenticationError{Kind: auth.InvalidCredential}
	}
	return principal, nil
}

var tokens = fakePort{
	"requester-token": {Subject: requester, Kind: auth.UserToken, Permissions: []auth.Permission{auth.UseChats}},
	"courier-token":   {Subject: courier, Kind: auth.UserToken, Permissions: []auth.Permission{auth.UseChats}},
	"outsider-token":  {Subject: outsider, Kind: auth.UserToken, Permissions: []auth.Permission{auth.UseChats}},
	// A real user whose role lacks chats:use.
	"no-permission-token": {Subject: requester, Kind: auth.UserToken},
	// A machine-to-machine token, even with the permission, is not a user.
	"service-token": {Subject: "client@clients", Kind: auth.ServiceToken, Permissions: []auth.Permission{auth.UseChats}},
}

type fixture struct {
	handler http.Handler
	store   *chattest.Store
	clock   *chattest.Clock
	room    chat.Room
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	store := &chattest.Store{}
	clock := chattest.NewClock(start)
	service := chat.NewService(store, chat.Options{Clock: clock.Now})
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	handler := NewRouter(NewChatHandler(service, auth.NewMiddleware(tokens), logger))
	room := store.AddRoom(uuid.New(), requester, courier)
	return &fixture{handler: handler, store: store, clock: clock, room: room}
}

func (f *fixture) do(t *testing.T, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, request)
	return recorder
}

func (f *fixture) messagesPath() string { return "/chats/" + f.room.ID.String() + "/messages" }

func decode[T any](t *testing.T, recorder *httptest.ResponseRecorder) T {
	t.Helper()
	require.Equal(t, "application/json", recorder.Header().Get("Content-Type"))
	var out T
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &out), recorder.Body.String())
	return out
}

func requireError(t *testing.T, recorder *httptest.ResponseRecorder, status int, code apperror.Code) {
	t.Helper()
	require.Equal(t, status, recorder.Code, recorder.Body.String())
	body := decode[apperror.Error](t, recorder)
	require.Equal(t, code, body.Code)
	require.NotEmpty(t, body.Message)
}

type roomList struct {
	Items []map[string]any `json:"items"`
}

type messageList struct {
	Items []protocol.Message `json:"items"`
}

func TestEveryRouteNeedsAUserTokenWithChatsUse(t *testing.T) {
	f := newFixture(t)
	routes := []struct{ method, path, body string }{
		{http.MethodGet, "/me/chats", ""},
		{http.MethodGet, "/errands/" + f.room.ErrandID.String() + "/chats", ""},
		{http.MethodGet, f.messagesPath(), ""},
		{http.MethodPost, f.messagesPath(), `{"clientMsgId":"c1","body":"hi"}`},
	}
	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			requireError(t, f.do(t, route.method, route.path, "", route.body), http.StatusUnauthorized, apperror.Unauthenticated)
			requireError(t, f.do(t, route.method, route.path, "forged-token", route.body), http.StatusUnauthorized, apperror.Unauthenticated)
			requireError(t, f.do(t, route.method, route.path, "no-permission-token", route.body), http.StatusForbidden, apperror.Forbidden)
			requireError(t, f.do(t, route.method, route.path, "service-token", route.body), http.StatusForbidden, apperror.Forbidden)
		})
	}
	require.Empty(t, f.store.Messages(), "nothing was sent")
}

func TestSendAndReadMessages(t *testing.T) {
	f := newFixture(t)

	created := f.do(t, http.MethodPost, f.messagesPath(), "requester-token", `{"clientMsgId":"c1","body":"I'm at the lobby"}`)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	sent := decode[protocol.Message](t, created)
	require.Equal(t, "I'm at the lobby", sent.Body)
	require.Equal(t, requester, sent.SenderID)
	require.Equal(t, chat.RoleRequester, sent.SenderRole)
	require.Equal(t, "c1", sent.ClientMsgID)
	require.Equal(t, f.room.ID, sent.RoomID)

	retry := f.do(t, http.MethodPost, f.messagesPath(), "requester-token", `{"clientMsgId":"c1","body":"I'm at the lobby"}`)
	require.Equal(t, http.StatusOK, retry.Code, "a retry returns the stored message")
	require.Equal(t, sent.ID, decode[protocol.Message](t, retry).ID)

	reply := f.do(t, http.MethodPost, f.messagesPath(), "courier-token", `{"clientMsgId":"c2","body":"On my way up"}`)
	require.Equal(t, http.StatusCreated, reply.Code)

	listed := f.do(t, http.MethodGet, f.messagesPath(), "courier-token", "")
	require.Equal(t, http.StatusOK, listed.Code)
	require.Equal(t, "no-store", listed.Header().Get("Cache-Control"))
	messages := decode[messageList](t, listed).Items
	require.Len(t, messages, 2)
	require.Equal(t, chat.RoleRequester, messages[0].SenderRole)
	require.Equal(t, chat.RoleCourier, messages[1].SenderRole)

	after := f.do(t, http.MethodGet, f.messagesPath()+"?after="+jsonNumber(sent.ID), "requester-token", "")
	require.Equal(t, http.StatusOK, after.Code)
	require.Equal(t, []string{"On my way up"}, bodies(decode[messageList](t, after).Items))

	before := f.do(t, http.MethodGet, f.messagesPath()+"?before="+jsonNumber(messages[1].ID)+"&limit=1", "requester-token", "")
	require.Equal(t, http.StatusOK, before.Code)
	require.Equal(t, []string{"I'm at the lobby"}, bodies(decode[messageList](t, before).Items))
}

func TestRoomLists(t *testing.T) {
	f := newFixture(t)
	f.do(t, http.MethodPost, f.messagesPath(), "courier-token", `{"clientMsgId":"c1","body":"accepted"}`)

	mine := f.do(t, http.MethodGet, "/me/chats", "requester-token", "")
	require.Equal(t, http.StatusOK, mine.Code)
	rooms := decode[roomList](t, mine).Items
	require.Len(t, rooms, 1)
	room := rooms[0]
	require.Equal(t, f.room.ID.String(), room["id"])
	require.Equal(t, f.room.ErrandID.String(), room["errandId"])
	require.Equal(t, "REQUESTER", room["myRole"])
	require.Equal(t, "OPEN", room["status"])
	require.Nil(t, room["closesAt"], "closesAt is present and null while open")
	require.Contains(t, room, "closesAt")
	require.Equal(t, "accepted", room["lastMessage"].(map[string]any)["body"])
	require.Equal(t, "COURIER", room["lastMessage"].(map[string]any)["senderRole"])

	forErrand := f.do(t, http.MethodGet, "/errands/"+f.room.ErrandID.String()+"/chats", "courier-token", "")
	require.Equal(t, http.StatusOK, forErrand.Code)
	rooms = decode[roomList](t, forErrand).Items
	require.Len(t, rooms, 1)
	require.Equal(t, "COURIER", rooms[0]["myRole"])
	require.NotContains(t, rooms[0], "lastMessage", "only /me/chats includes it")

	for _, path := range []string{"/me/chats", "/errands/" + f.room.ErrandID.String() + "/chats", "/errands/" + uuid.NewString() + "/chats"} {
		empty := f.do(t, http.MethodGet, path, "outsider-token", "")
		require.Equal(t, http.StatusOK, empty.Code)
		require.JSONEq(t, `{"items":[]}`, empty.Body.String(), "an empty list, never null")
	}
}

func TestClosingAndClosedRoomsOverHTTP(t *testing.T) {
	f := newFixture(t)
	endedAt := start.Add(time.Hour)
	f.store.EndRoom(f.room.ID, chat.EndCompleted, endedAt, 24*time.Hour)

	f.clock.Set(endedAt.Add(time.Hour))
	rooms := decode[roomList](t, f.do(t, http.MethodGet, "/me/chats", "courier-token", "")).Items
	require.Equal(t, "CLOSING", rooms[0]["status"])
	require.Equal(t, "COMPLETED", rooms[0]["endReason"])
	require.Equal(t, "2026-11-10T15:00:00Z", rooms[0]["closesAt"])
	require.Equal(t, http.StatusCreated, f.do(t, http.MethodPost, f.messagesPath(), "courier-token", `{"clientMsgId":"c1","body":"left it with the guard"}`).Code)

	f.clock.Set(endedAt.Add(24*time.Hour + time.Second))
	requireError(t, f.do(t, http.MethodPost, f.messagesPath(), "requester-token", `{"clientMsgId":"c2","body":"thanks"}`),
		http.StatusConflict, apperror.RoomClosed)
	read := f.do(t, http.MethodGet, f.messagesPath(), "requester-token", "")
	require.Equal(t, http.StatusOK, read.Code, "reading still works")
	require.Len(t, decode[messageList](t, read).Items, 1)
}

func TestOutsidersAndUnknownRoomsGetNotFound(t *testing.T) {
	f := newFixture(t)
	unknown := "/chats/" + uuid.NewString() + "/messages"
	for _, tc := range []struct{ token, path string }{
		{"outsider-token", f.messagesPath()},
		{"requester-token", unknown},
	} {
		requireError(t, f.do(t, http.MethodGet, tc.path, tc.token, ""), http.StatusNotFound, apperror.RoomNotFound)
		requireError(t, f.do(t, http.MethodPost, tc.path, tc.token, `{"clientMsgId":"c1","body":"hi"}`), http.StatusNotFound, apperror.RoomNotFound)
	}
	require.Empty(t, f.store.Messages())
}

func TestBadRequests(t *testing.T) {
	f := newFixture(t)
	path := f.messagesPath()
	cases := []struct {
		name, method, path, body string
		code                     apperror.Code
	}{
		{"room ID not a UUID", http.MethodGet, "/chats/not-a-uuid/messages", "", apperror.InvalidArgument},
		{"errand ID not a UUID", http.MethodGet, "/errands/42/chats", "", apperror.InvalidArgument},
		{"after not a number", http.MethodGet, path + "?after=abc", "", apperror.InvalidArgument},
		{"after and before", http.MethodGet, path + "?after=1&before=5", "", apperror.InvalidArgument},
		{"limit too large", http.MethodGet, path + "?limit=101", "", apperror.InvalidArgument},
		{"limit zero", http.MethodGet, path + "?limit=0", "", apperror.InvalidArgument},
		{"no body", http.MethodPost, path, "", apperror.InvalidArgument},
		{"not JSON", http.MethodPost, path, "hello", apperror.InvalidArgument},
		{"unknown field", http.MethodPost, path, `{"clientMsgId":"c1","body":"hi","senderId":"auth0|admin"}`, apperror.InvalidArgument},
		{"two objects", http.MethodPost, path, `{"clientMsgId":"c1","body":"hi"}{}`, apperror.InvalidArgument},
		{"too large", http.MethodPost, path, `{"clientMsgId":"c1","body":"` + strings.Repeat("a", maxBodyBytes) + `"}`, apperror.InvalidArgument},
		{"empty body text", http.MethodPost, path, `{"clientMsgId":"c1","body":"   "}`, apperror.InvalidMessage},
		{"message too long", http.MethodPost, path, `{"clientMsgId":"c1","body":"` + strings.Repeat("a", 2001) + `"}`, apperror.InvalidMessage},
		{"missing clientMsgId", http.MethodPost, path, `{"body":"hi"}`, apperror.InvalidMessage},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireError(t, f.do(t, tc.method, tc.path, "requester-token", tc.body), http.StatusBadRequest, tc.code)
		})
	}
	require.Empty(t, f.store.Messages())

	recorder := f.do(t, http.MethodPost, path, "requester-token", `{"clientMsgId":"c1","body":"`+strings.Repeat("a", 2001)+`"}`)
	require.Equal(t, "body must be 1 to 2000 characters", decode[apperror.Error](t, recorder).Message,
		"the rule is explained to the client")
}

func TestStorageOutageIs503WithoutDetails(t *testing.T) {
	f := newFixture(t)
	f.store.FailWith(errors.Join(chat.ErrUnavailable, errors.New("dial tcp chat-db:5432: password=s3cret")))
	for _, recorder := range []*httptest.ResponseRecorder{
		f.do(t, http.MethodGet, "/me/chats", "requester-token", ""),
		f.do(t, http.MethodPost, f.messagesPath(), "requester-token", `{"clientMsgId":"c1","body":"hi"}`),
	} {
		requireError(t, recorder, http.StatusServiceUnavailable, apperror.DependencyUnavailable)
		require.NotContains(t, recorder.Body.String(), "s3cret")
		require.NotContains(t, recorder.Body.String(), "chat-db")
	}

	f.store.FailWith(errors.New("something unexpected"))
	recorder := f.do(t, http.MethodGet, "/me/chats", "requester-token", "")
	requireError(t, recorder, http.StatusInternalServerError, apperror.Internal)
	require.NotContains(t, recorder.Body.String(), "something", "the cause is logged, not returned")
}

func bodies(messages []protocol.Message) []string {
	out := make([]string, len(messages))
	for i, m := range messages {
		out[i] = m.Body
	}
	return out
}

func jsonNumber(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
