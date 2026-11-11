// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-10
// Scope: Implementation of test code (unit and integration tests).
// Author review: Reviewed, validated, and tested by the authors.

package app

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/auth/authtest"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/events"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/testdb"
)

// TestRoomClosingEndToEnd is the phase 7 check in code, with
// CHAT_CLOSE_DELAY set to two seconds: the errand completes while both people
// are connected, both are told the room is closing, it stays writable until
// closesAt, and is read-only (but still readable) after it.
func TestRoomClosingEndToEnd(t *testing.T) {
	amqpURL := os.Getenv("TEST_AMQP_URL")
	if amqpURL == "" {
		t.Skip("TEST_AMQP_URL is not set; skipping RabbitMQ test")
	}
	const closeDelay = 2 * time.Second
	ctx := context.Background()
	db := testdb.Migrated(t)
	keys := authtest.New(t)
	cfg := testConfig(db.URL)
	cfg.AMQP.URL = amqpURL
	cfg.Auth.Issuer = keys.Server.URL + "/"
	cfg.HTTP.Port = freePort(t)
	cfg.Chat.CloseDelay = closeDelay
	application, err := New(ctx, cfg, discardLogger(), auth.WithHTTPClient(keys.Server.Client()))
	require.NoError(t, err)
	t.Cleanup(application.Close)

	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- application.Run(runCtx) }()
	t.Cleanup(func() {
		stop()
		<-done
	})
	base := "127.0.0.1:" + strconv.Itoa(cfg.HTTP.Port)
	waitSubscribed(t, base)

	token := func(subject string) string {
		claims := keys.Claims(auth.ChatAudience, []string{"chats:use"})
		claims["sub"] = subject
		return keys.Token(t, claims, "one")
	}
	connect := func(subject string) *websocket.Conn {
		dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		conn, _, err := websocket.Dial(dialCtx, "ws://"+base+"/ws", &websocket.DialOptions{
			HTTPHeader: http.Header{"Origin": []string{"http://localhost:8081"}},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.CloseNow() })
		write(t, conn, map[string]string{"type": "auth", "token": token(subject)})
		require.Equal(t, "ready", read(t, conn)["type"])
		return conn
	}
	// readUntil skips frames of other types (acks, messages) until one of
	// frameType arrives.
	readUntil := func(conn *websocket.Conn, frameType string) map[string]any {
		t.Helper()
		for {
			if frame := read(t, conn); frame["type"] == frameType {
				return frame
			}
		}
	}
	roomStatus := func(errandID string) string {
		t.Helper()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+base+"/errands/"+errandID+"/chats", nil)
		require.NoError(t, err)
		request.Header.Set("Authorization", "Bearer "+token("auth0|requester"))
		response, err := http.DefaultClient.Do(request)
		require.NoError(t, err)
		defer response.Body.Close()
		require.Equal(t, http.StatusOK, response.StatusCode)
		var list struct {
			Items []struct {
				Status string `json:"status"`
			} `json:"items"`
		}
		require.NoError(t, json.NewDecoder(response.Body).Decode(&list))
		require.Len(t, list.Items, 1)
		return list.Items[0].Status
	}

	requester := connect("auth0|requester")
	courier := connect("auth0|courier")

	errandID := uuid.NewString()
	publishErrandEvent(t, amqpURL, events.RoutingErrandAccepted, errandID, 3, time.Now())
	roomID := readUntil(courier, "room_opened")["room"].(map[string]any)["id"].(string)
	readUntil(requester, "room_opened")
	require.Equal(t, "OPEN", roomStatus(errandID))

	// The requester confirms delivery: order-service publishes errand.completed.
	completedAt := time.Now()
	publishErrandEvent(t, amqpURL, events.RoutingErrandCompleted, errandID, 6, completedAt)
	for name, conn := range map[string]*websocket.Conn{"requester": requester, "courier": courier} {
		frame := readUntil(conn, "room_closing")
		require.Equal(t, roomID, frame["roomId"], name)
		require.Equal(t, errandID, frame["errandId"], name)
		require.Equal(t, "COMPLETED", frame["endReason"], name)
		closesAt, err := time.Parse(time.RFC3339Nano, frame["closesAt"].(string))
		require.NoError(t, err)
		require.WithinDuration(t, completedAt.Add(closeDelay), closesAt, time.Millisecond,
			"closesAt is the event's occurredAt plus the delay")
	}
	require.Equal(t, "CLOSING", roomStatus(errandID))

	// Still writable while CLOSING.
	write(t, courier, map[string]string{"type": "send", "roomId": roomID, "clientMsgId": "c1", "body": "Left it with the guard"})
	require.Equal(t, "c1", readUntil(courier, "ack")["clientMsgId"])

	// After closesAt: read-only.
	time.Sleep(time.Until(completedAt.Add(closeDelay)) + 100*time.Millisecond)
	require.Equal(t, "CLOSED", roomStatus(errandID))
	write(t, requester, map[string]string{"type": "send", "roomId": roomID, "clientMsgId": "c2", "body": "Thanks!"})
	refused := readUntil(requester, "error")
	require.Equal(t, "ROOM_CLOSED", refused["code"])
	require.Equal(t, "c2", refused["clientMsgId"])

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+base+"/chats/"+roomID+"/messages", nil)
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer "+token("auth0|requester"))
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	var history struct {
		Items []map[string]any `json:"items"`
	}
	require.NoError(t, json.NewDecoder(response.Body).Decode(&history))
	_ = response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode, "a CLOSED room is still readable")
	require.Len(t, history.Items, 1)
}
