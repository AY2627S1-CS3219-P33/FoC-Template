// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-10
// Scope: Implementation of test code (unit and integration tests).
// Author review: Reviewed, validated, and tested by the authors.

package broadcast

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/chat"
)

// fakeHub records deliveries.
type fakeHub struct {
	mu         sync.Mutex
	deliveries []delivery
}

type delivery struct {
	to      []string
	payload map[string]any
}

func (f *fakeHub) Deliver(userIDs []string, payload []byte) {
	var decoded map[string]any
	_ = json.Unmarshal(payload, &decoded)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deliveries = append(f.deliveries, delivery{append([]string(nil), userIDs...), decoded})
}

var (
	now  = time.Date(2026, 11, 9, 14, 0, 0, 0, time.UTC)
	room = chat.Room{
		ID: uuid.New(), ErrandID: uuid.New(),
		RequesterID: "auth0|requester", CourierID: "auth0|courier", CreatedAt: now,
	}
	logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
)

// Plan, section 9: LocalBroadcaster delivers to exactly the listed recipients.
func TestLocalPublishDeliversToExactlyTheRecipients(t *testing.T) {
	hub := &fakeHub{}
	local := NewLocal(hub)
	require.NoError(t, local.Publish(context.Background(), Envelope{
		Recipients: []string{"auth0|a", "auth0|b"}, Payload: json.RawMessage(`{"type":"x"}`),
	}))
	require.Len(t, hub.deliveries, 1)
	require.Equal(t, []string{"auth0|a", "auth0|b"}, hub.deliveries[0].to)
	require.Equal(t, "x", hub.deliveries[0].payload["type"])
}

func TestLocalRunWaitsForCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- NewLocal(&fakeHub{}).Run(ctx) }()
	select {
	case <-done:
		t.Fatal("Run returned before cancellation")
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
}

func TestMessageSentGoesToBothParticipants(t *testing.T) {
	hub := &fakeHub{}
	notifier := NewNotifier(NewLocal(hub), func() time.Time { return now }, logger)
	notifier.MessageSent(context.Background(), room, chat.MessageView{
		Message:    chat.Message{ID: 7, RoomID: room.ID, SenderID: room.CourierID, Body: "on my way", ClientMsgID: "c1", CreatedAt: now},
		SenderRole: chat.RoleCourier,
	})
	require.Len(t, hub.deliveries, 1)
	got := hub.deliveries[0]
	require.ElementsMatch(t, []string{room.RequesterID, room.CourierID}, got.to)
	require.Equal(t, "message", got.payload["type"])
	message := got.payload["message"].(map[string]any)
	require.Equal(t, "on my way", message["body"])
	require.Equal(t, "COURIER", message["senderRole"])
	require.EqualValues(t, 7, message["id"])
}

func TestRoomOpenedIsPersonalised(t *testing.T) {
	hub := &fakeHub{}
	notifier := NewNotifier(NewLocal(hub), func() time.Time { return now }, logger)
	notifier.RoomOpened(context.Background(), room)

	require.Len(t, hub.deliveries, 2, "one frame per participant")
	roles := map[string]any{}
	for _, d := range hub.deliveries {
		require.Len(t, d.to, 1)
		require.Equal(t, "room_opened", d.payload["type"])
		r := d.payload["room"].(map[string]any)
		require.Equal(t, room.ID.String(), r["id"])
		require.Equal(t, "OPEN", r["status"])
		roles[d.to[0]] = r["myRole"]
	}
	require.Equal(t, map[string]any{room.RequesterID: "REQUESTER", room.CourierID: "COURIER"}, roles)
}

func TestRoomClosing(t *testing.T) {
	hub := &fakeHub{}
	notifier := NewNotifier(NewLocal(hub), nil, logger)
	notifier.RoomClosing(context.Background(), room)
	require.Empty(t, hub.deliveries, "an open room is not closing")

	ending := room
	ending.Ending = &chat.Ending{Reason: chat.EndCompleted, EndedAt: now, ClosesAt: now.Add(24 * time.Hour)}
	notifier.RoomClosing(context.Background(), ending)
	require.Len(t, hub.deliveries, 1)
	got := hub.deliveries[0]
	require.ElementsMatch(t, []string{room.RequesterID, room.CourierID}, got.to)
	require.Equal(t, map[string]any{
		"type": "room_closing", "roomId": room.ID.String(), "errandId": room.ErrandID.String(),
		"endReason": "COMPLETED", "closesAt": "2026-11-10T14:00:00Z",
	}, got.payload)
}

type failingBroadcaster struct{}

func (failingBroadcaster) Publish(context.Context, Envelope) error { return errors.New("redis down") }
func (failingBroadcaster) Run(context.Context) error               { return nil }

func TestAFailedPublishNeverFailsTheCaller(t *testing.T) {
	notifier := NewNotifier(failingBroadcaster{}, nil, logger)
	require.NotPanics(t, func() {
		notifier.MessageSent(context.Background(), room, chat.MessageView{})
		notifier.RoomOpened(context.Background(), room)
	})
}
