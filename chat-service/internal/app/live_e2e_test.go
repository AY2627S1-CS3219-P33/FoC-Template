// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-10
// Scope: Implementation of test code (unit and integration tests).
// Author review: Reviewed, validated, and tested by the authors.

package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/auth/authtest"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/events"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/testdb"
)

// TestLiveChatEndToEnd is the phase 5 check in code, with every real part
// except Auth0 (tokens come from a local key server): the requester and the
// courier are connected over WebSockets when the courier accepts the errand;
// both are told about the new room, then chat live, over WebSocket and REST.
func TestLiveChatEndToEnd(t *testing.T) {
	amqpURL := os.Getenv("TEST_AMQP_URL")
	if amqpURL == "" {
		t.Skip("TEST_AMQP_URL is not set; skipping RabbitMQ test")
	}
	ctx := context.Background()
	db := testdb.Migrated(t)
	keys := authtest.New(t)
	cfg := testConfig(db.URL)
	cfg.AMQP.URL = amqpURL
	cfg.Auth.Issuer = keys.Server.URL + "/"
	cfg.HTTP.Port = freePort(t)
	application, err := New(ctx, cfg, discardLogger(), auth.WithHTTPClient(keys.Server.Client()))
	require.NoError(t, err)
	t.Cleanup(application.Close)

	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- application.Run(runCtx) }()
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
	requester := connect("auth0|requester")
	courier := connect("auth0|courier")

	// The courier accepts: order-service publishes errand.accepted.
	errandID := uuid.NewString()
	publishAccepted(t, amqpURL, errandID)
	opened := map[string]any{}
	for name, conn := range map[string]*websocket.Conn{"requester": requester, "courier": courier} {
		frame := read(t, conn)
		require.Equal(t, "room_opened", frame["type"], name)
		opened[name] = frame["room"]
	}
	room := opened["courier"].(map[string]any)
	require.Equal(t, errandID, room["errandId"])
	require.Equal(t, "COURIER", room["myRole"])
	require.Equal(t, "REQUESTER", opened["requester"].(map[string]any)["myRole"])
	roomID := room["id"].(string)

	// Live over WebSocket.
	write(t, courier, map[string]string{"type": "send", "roomId": roomID, "clientMsgId": "c1", "body": "On my way up"})
	got := read(t, requester)
	require.Equal(t, "message", got["type"])
	require.Equal(t, "On my way up", got["message"].(map[string]any)["body"])
	require.Equal(t, "COURIER", got["message"].(map[string]any)["senderRole"])

	// A REST send is pushed live too.
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+base+"/chats/"+roomID+"/messages",
		strings.NewReader(`{"clientMsgId":"c2","body":"I'm at the lobby"}`))
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer "+token("auth0|requester"))
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	_ = response.Body.Close()
	require.Equal(t, http.StatusCreated, response.StatusCode)
	for {
		frame := read(t, courier) // the courier also has its own ack and message frames queued
		if frame["type"] == "message" && frame["message"].(map[string]any)["body"] == "I'm at the lobby" {
			break
		}
	}

	// Shutdown tells every browser to reconnect.
	stop()
	for _, conn := range []*websocket.Conn{requester, courier} {
		readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		for {
			if _, _, err := conn.Read(readCtx); err != nil {
				require.Equal(t, websocket.StatusCode(1012), websocket.CloseStatus(err))
				break
			}
		}
		cancel()
	}
	require.NoError(t, <-done)
}

func publishAccepted(t *testing.T, amqpURL, errandID string) {
	t.Helper()
	publishErrandEvent(t, amqpURL, events.RoutingErrandAccepted, errandID, 3,
		time.Date(2026, 11, 9, 14, 0, 0, 0, time.UTC))
}

// publishErrandEvent publishes an errand event between auth0|requester and
// auth0|courier, as order-service's outbox dispatcher does.
func publishErrandEvent(t *testing.T, amqpURL, routingKey, errandID string, version int, occurredAt time.Time) {
	t.Helper()
	conn, err := amqp.Dial(amqpURL)
	require.NoError(t, err)
	defer conn.Close()
	ch, err := conn.Channel()
	require.NoError(t, err)
	at := occurredAt.UTC().Format(time.RFC3339Nano)
	messageID := errandID + ":" + routingKey + ":" + strconv.Itoa(version)
	body, err := json.Marshal(map[string]any{
		"messageId": messageID, "type": routingKey,
		"occurredAt": at, "errandId": errandID, "errandVersion": version,
		"data": map[string]any{
			"errandId": errandID, "errandVersion": version, "event": routingKey, "to": "ASSIGNED",
			"actorType": "COURIER", "actorId": "auth0|courier",
			"requesterId": "auth0|requester", "courierId": "auth0|courier",
			"reward": 5, "at": at,
		},
	})
	require.NoError(t, err)
	require.NoError(t, ch.PublishWithContext(context.Background(), events.Exchange, routingKey, true, false,
		amqp.Publishing{ContentType: "application/json", DeliveryMode: amqp.Persistent,
			MessageId: messageID, Type: routingKey, Body: body}))
}

// waitSubscribed waits until /readyz reports the errand-event consumer as
// subscribed. An open HTTP port is not enough: until the consumer has
// declared its queue, a published event has nowhere to go and RabbitMQ drops
// it (on a fresh broker, the first test in this package would hang).
func waitSubscribed(t *testing.T, base string) {
	t.Helper()
	require.Eventually(t, func() bool {
		response, err := http.Get("http://" + base + "/readyz")
		if err != nil {
			return false
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		return response.StatusCode == http.StatusOK && strings.Contains(string(body), `"errandEvents":"ok"`)
	}, 15*time.Second, 50*time.Millisecond, "the consumer subscribes and /readyz reports it")
}

func write(t *testing.T, conn *websocket.Conn, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, conn.Write(ctx, websocket.MessageText, data))
}

func read(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, data, err := conn.Read(ctx)
	require.NoError(t, err)
	var frame map[string]any
	require.NoError(t, json.Unmarshal(data, &frame))
	return frame
}
