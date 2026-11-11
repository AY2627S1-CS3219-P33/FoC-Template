// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-10
// Scope: Implementation of test code (unit and integration tests).
// Author review: Reviewed, validated, and tested by the authors.

package ws

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/broadcast"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/chat"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/chat/chattest"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/hub"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/protocol"
)

const (
	requester = "auth0|requester"
	courier   = "auth0|courier"
	outsider  = "auth0|outsider"
)

// fakePort accepts the tokens in its map. auth's own tests cover real JWTs.
type fakePort map[string]auth.Principal

func (p fakePort) Authenticate(_ context.Context, token string) (auth.Principal, error) {
	if token == "auth0-down" {
		return auth.Principal{}, &auth.AuthenticationError{Kind: auth.VerifierUnavailable}
	}
	principal, ok := p[token]
	if !ok {
		return auth.Principal{}, &auth.AuthenticationError{Kind: auth.InvalidCredential}
	}
	return principal, nil
}

func user(subject string, valid time.Duration) auth.Principal {
	return auth.Principal{Subject: subject, Kind: auth.UserToken,
		Permissions: []auth.Permission{auth.UseChats}, ExpiresAt: time.Now().Add(valid)}
}

type fixture struct {
	server   *httptest.Server
	handler  *Handler
	store    *chattest.Store
	notifier *broadcast.Notifier
	room     chat.Room
}

func newFixture(t *testing.T, change func(*Config)) *fixture {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	tokens := fakePort{
		"requester-token":     user(requester, time.Hour),
		"requester-token-2":   user(requester, time.Hour),
		"courier-token":       user(courier, time.Hour),
		"outsider-token":      user(outsider, time.Hour),
		"short-lived-token":   user(courier, 300*time.Millisecond),
		"no-permission-token": {Subject: requester, Kind: auth.UserToken, ExpiresAt: time.Now().Add(time.Hour)},
		"service-token": {Subject: "client@clients", Kind: auth.ServiceToken,
			Permissions: []auth.Permission{auth.UseChats}, ExpiresAt: time.Now().Add(time.Hour)},
	}
	store := &chattest.Store{}
	connections := hub.New(2)
	notifier := broadcast.NewNotifier(broadcast.NewLocal(connections), nil, logger)
	service := chat.NewService(store, chat.Options{Notifier: notifier})
	cfg := Config{
		AllowedOrigins: []string{"app.bestfoc.app"},
		AuthTimeout:    2 * time.Second,
		PingInterval:   time.Minute,
		SendRate:       100,
		SendBurst:      100,
	}
	if change != nil {
		change(&cfg)
	}
	handler := NewHandler(tokens, service, connections, cfg, logger)
	router := http.NewServeMux()
	handler.RegisterRoutes(router)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return &fixture{server: server, handler: handler, store: store, notifier: notifier,
		room: store.AddRoom(uuid.New(), requester, courier)}
}

func (f *fixture) dialRaw(t *testing.T, origin string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	header := http.Header{}
	if origin != "" {
		header.Set("Origin", origin)
	}
	conn, response, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(f.server.URL, "http")+"/ws",
		&websocket.DialOptions{HTTPHeader: header})
	if conn != nil {
		t.Cleanup(func() { _ = conn.CloseNow() })
	}
	return conn, response, err
}

// connect dials, authenticates and waits for ready.
func (f *fixture) connect(t *testing.T, token string) *websocket.Conn {
	t.Helper()
	conn, _, err := f.dialRaw(t, "https://app.bestfoc.app")
	require.NoError(t, err)
	writeJSON(t, conn, map[string]string{"type": "auth", "token": token})
	ready := readFrame(t, conn)
	require.Equal(t, "ready", ready["type"])
	return conn
}

func writeJSON(t *testing.T, conn *websocket.Conn, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, conn.Write(ctx, websocket.MessageText, data))
}

func readFrame(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, data, err := conn.Read(ctx)
	require.NoError(t, err)
	var frame map[string]any
	require.NoError(t, json.Unmarshal(data, &frame))
	return frame
}

// readFrames reads until it has one frame of each wanted type, in any order.
func readFrames(t *testing.T, conn *websocket.Conn, types ...string) map[string]map[string]any {
	t.Helper()
	got := map[string]map[string]any{}
	for len(got) < len(types) {
		frame := readFrame(t, conn)
		got[frame["type"].(string)] = frame
	}
	for _, typ := range types {
		require.Contains(t, got, typ)
	}
	return got
}

// requireNoFrame checks nothing arrives for a while. The read is cancelled
// afterwards, which closes the connection, so call it last on a connection.
func requireNoFrame(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, data, err := conn.Read(ctx)
	require.Error(t, err, "unexpected frame: %s", data)
}

// requireClosedWith reads until the server closes the connection and checks
// the close code.
func requireClosedWith(t *testing.T, conn *websocket.Conn, code websocket.StatusCode) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		_, _, err := conn.Read(ctx)
		if err != nil {
			require.Equal(t, code, websocket.CloseStatus(err), "close error: %v", err)
			return
		}
	}
}

func send(t *testing.T, conn *websocket.Conn, roomID uuid.UUID, clientMsgID, body string) {
	t.Helper()
	writeJSON(t, conn, map[string]string{"type": "send", "roomId": roomID.String(), "clientMsgId": clientMsgID, "body": body})
}

// Plan, section 10: A sends, B receives message and A receives ack; a third
// user gets nothing.
func TestMessagesAreDeliveredLive(t *testing.T) {
	f := newFixture(t, nil)
	a := f.connect(t, "requester-token")
	aOtherTab := f.connect(t, "requester-token-2")
	b := f.connect(t, "courier-token")
	stranger := f.connect(t, "outsider-token")

	send(t, a, f.room.ID, "c1", "I'm at the lobby")

	atSender := readFrames(t, a, "ack", "message")
	require.Equal(t, "c1", atSender["ack"]["clientMsgId"])
	acked := atSender["ack"]["message"].(map[string]any)
	require.Equal(t, "I'm at the lobby", acked["body"])
	require.Equal(t, "REQUESTER", acked["senderRole"])

	atCourier := readFrame(t, b)
	require.Equal(t, "message", atCourier["type"])
	require.Equal(t, acked, atCourier["message"], "both see the same stored message")

	require.Equal(t, "message", readFrame(t, aOtherTab)["type"], "the sender's other tabs see it too")
	requireNoFrame(t, stranger)
}

// Plan, section 10: a resend with the same clientMsgId produces one row.
func TestAResendStoresOneMessage(t *testing.T) {
	f := newFixture(t, nil)
	a := f.connect(t, "requester-token")
	b := f.connect(t, "courier-token")

	send(t, a, f.room.ID, "c1", "hello")
	first := readFrames(t, a, "ack", "message")["ack"]
	send(t, a, f.room.ID, "c1", "hello")
	second := readFrame(t, a)
	require.Equal(t, "ack", second["type"], "the retry is acknowledged again")
	require.Equal(t, first["message"], second["message"])

	require.Len(t, f.store.Messages(), 1)
	require.Equal(t, "message", readFrame(t, b)["type"])
	requireNoFrame(t, b) // and no second message frame
}

// Plan, section 10: no auth frame within the timeout closes with 4401.
func TestNoAuthFrameInTimeClosesWith4401(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.AuthTimeout = 200 * time.Millisecond })
	conn, _, err := f.dialRaw(t, "")
	require.NoError(t, err)
	requireClosedWith(t, conn, protocol.CloseUnauthenticated)
}

// Plan, section 10: sending to a CLOSED room returns error ROOM_CLOSED.
func TestSendingToAClosedRoomIsRefused(t *testing.T) {
	f := newFixture(t, nil)
	f.store.EndRoom(f.room.ID, chat.EndCompleted, time.Now().Add(-48*time.Hour), 24*time.Hour)
	a := f.connect(t, "requester-token")

	send(t, a, f.room.ID, "c1", "too late")
	frame := readFrame(t, a)
	require.Equal(t, map[string]any{
		"type": "error", "code": "ROOM_CLOSED", "clientMsgId": "c1",
		"message": "this chat is closed; messages can no longer be sent",
	}, frame)
	require.Empty(t, f.store.Messages())
}

func TestRefusedSendsGetErrorFrames(t *testing.T) {
	f := newFixture(t, nil)
	a := f.connect(t, "requester-token")
	stranger := f.connect(t, "outsider-token")

	send(t, stranger, f.room.ID, "c1", "hi")
	require.Equal(t, "ROOM_NOT_FOUND", readFrame(t, stranger)["code"], "outsiders cannot tell the room exists")

	send(t, a, f.room.ID, "c2", "   ")
	frame := readFrame(t, a)
	require.Equal(t, "INVALID_MESSAGE", frame["code"])
	require.Equal(t, "c2", frame["clientMsgId"])

	writeJSON(t, a, map[string]string{"type": "send", "roomId": "nope", "clientMsgId": "c3", "body": "hi"})
	require.Equal(t, "INVALID_ARGUMENT", readFrame(t, a)["code"])

	writeJSON(t, a, map[string]string{"type": "dance"})
	require.Equal(t, "INVALID_ARGUMENT", readFrame(t, a)["code"])

	writeJSON(t, a, map[string]string{"type": "auth", "token": "requester-token"})
	require.Equal(t, "INVALID_ARGUMENT", readFrame(t, a)["code"], "already authenticated")

	require.Empty(t, f.store.Messages())
}

func TestAuthenticationFailures(t *testing.T) {
	cases := []struct {
		name  string
		frame any
		code  websocket.StatusCode
	}{
		{"unknown token", map[string]string{"type": "auth", "token": "forged"}, protocol.CloseUnauthenticated},
		{"first frame is not auth", map[string]string{"type": "send", "body": "hi"}, protocol.CloseUnauthenticated},
		{"empty token", map[string]string{"type": "auth"}, protocol.CloseUnauthenticated},
		{"no chats:use", map[string]string{"type": "auth", "token": "no-permission-token"}, protocol.CloseForbidden},
		{"machine token", map[string]string{"type": "auth", "token": "service-token"}, protocol.CloseForbidden},
		{"Auth0 unreachable", map[string]string{"type": "auth", "token": "auth0-down"}, protocol.CloseTryAgainLater},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, nil)
			conn, _, err := f.dialRaw(t, "")
			require.NoError(t, err)
			writeJSON(t, conn, tc.frame)
			requireClosedWith(t, conn, tc.code)
		})
	}
}

func TestOnlyAllowedOriginsMayConnect(t *testing.T) {
	f := newFixture(t, nil)
	_, response, err := f.dialRaw(t, "https://evil.example")
	require.Error(t, err)
	require.Equal(t, http.StatusForbidden, response.StatusCode, "another website cannot open a chat connection")

	f.connect(t, "requester-token") // allowed origin
	conn, _, err := f.dialRaw(t, "")
	require.NoError(t, err, "tools without an Origin header (websocat) may connect; they still need a token")
	_ = conn.CloseNow()
}

func TestTooManyConnectionsClosesWith4429(t *testing.T) {
	f := newFixture(t, nil) // hub allows 2 per user
	f.connect(t, "requester-token")
	f.connect(t, "requester-token-2")
	conn, _, err := f.dialRaw(t, "")
	require.NoError(t, err)
	writeJSON(t, conn, map[string]string{"type": "auth", "token": "requester-token"})
	requireClosedWith(t, conn, protocol.CloseTooManyConns)
}

func TestSendingTooFastIsRateLimited(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.SendRate, c.SendBurst = 0.001, 2 })
	a := f.connect(t, "requester-token")
	for i, id := range []string{"c1", "c2", "c3"} {
		send(t, a, f.room.ID, id, "spam")
		if i < 2 {
			readFrames(t, a, "ack", "message")
			continue
		}
		frame := readFrame(t, a)
		require.Equal(t, "RATE_LIMITED", frame["code"])
		require.Equal(t, "c3", frame["clientMsgId"])
	}
	require.Len(t, f.store.Messages(), 2)
}

func TestAnExpiredTokenClosesWith4401(t *testing.T) {
	f := newFixture(t, nil)
	conn := f.connect(t, "short-lived-token") // expires after 300 ms
	requireClosedWith(t, conn, protocol.CloseUnauthenticated)
}

func TestMalformedFramesCloseWith1008(t *testing.T) {
	f := newFixture(t, nil)
	conn := f.connect(t, "requester-token")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, conn.Write(ctx, websocket.MessageText, []byte("not json")))
	requireClosedWith(t, conn, protocol.ClosePolicyViolation)
}

func TestHeartbeat(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.PingInterval = 50 * time.Millisecond })

	// A client that keeps reading answers pings and stays connected.
	alive := f.connect(t, "requester-token")
	frames := make(chan map[string]any, 1)
	go func() {
		_, data, err := alive.Read(context.Background())
		if err == nil {
			var frame map[string]any
			_ = json.Unmarshal(data, &frame)
			frames <- frame
		}
	}()
	time.Sleep(400 * time.Millisecond) // several ping intervals
	f.notifier.RoomOpened(context.Background(), f.room)
	select {
	case frame := <-frames:
		require.Equal(t, "room_opened", frame["type"], "still connected after many pings")
	case <-time.After(3 * time.Second):
		t.Fatal("the connection died although the client answered pings")
	}

	// A client that never reads cannot answer pings: the server notices.
	dead := f.connect(t, "courier-token")
	require.Eventually(t, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		err := dead.Write(ctx, websocket.MessageText, []byte(`{"type":"noop"}`))
		return err != nil
	}, 5*time.Second, 100*time.Millisecond, "the server closed a connection that stopped answering pings")
}

func TestRoomOpenedReachesBothParticipants(t *testing.T) {
	f := newFixture(t, nil)
	a := f.connect(t, "requester-token")
	b := f.connect(t, "courier-token")
	f.notifier.RoomOpened(context.Background(), f.room)

	for conn, role := range map[*websocket.Conn]string{a: "REQUESTER", b: "COURIER"} {
		frame := readFrame(t, conn)
		require.Equal(t, "room_opened", frame["type"])
		require.Equal(t, role, frame["room"].(map[string]any)["myRole"])
	}
}

func TestShutdownClosesWith1012AndRefusesNewConnections(t *testing.T) {
	f := newFixture(t, nil)
	authenticated := f.connect(t, "requester-token")
	waiting, _, err := f.dialRaw(t, "") // still in the auth step
	require.NoError(t, err)

	shutdownDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownDone <- f.handler.Shutdown(ctx)
	}()
	requireClosedWith(t, authenticated, protocol.CloseServiceRestart)
	requireClosedWith(t, waiting, protocol.CloseServiceRestart)
	require.NoError(t, <-shutdownDone)

	_, response, err := f.dialRaw(t, "")
	require.Error(t, err)
	require.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
}

func TestNewHandlerChecksItsArguments(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	ok := Config{AuthTimeout: time.Second, PingInterval: time.Second, SendRate: 1, SendBurst: 1}
	service := chat.NewService(&chattest.Store{}, chat.Options{})
	require.Panics(t, func() { NewHandler(nil, service, hub.New(1), ok, logger) })
	bad := ok
	bad.SendBurst = 0
	require.Panics(t, func() { NewHandler(fakePort{}, service, hub.New(1), bad, logger) })
	require.NotPanics(t, func() { NewHandler(fakePort{}, service, hub.New(1), ok, logger) })
}
