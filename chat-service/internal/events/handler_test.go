package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/chat"
)

// acceptedBody is an errand.accepted message exactly as order-service's
// outbox dispatcher builds it (internal/errand/outbox.go, EventData).
func acceptedBody(t *testing.T, errandID string, change func(env map[string]any, data map[string]any)) []byte {
	t.Helper()
	data := map[string]any{
		"errandId": errandID, "errandVersion": 3, "event": "accept",
		"from": "OPEN", "to": "ASSIGNED", "actorType": "COURIER", "actorId": "auth0|courier",
		"requesterId": "auth0|requester", "courierId": "auth0|courier",
		"reward": 5, "at": "2026-11-09T14:00:00Z",
	}
	env := map[string]any{
		"messageId": errandID + ":accepted:3", "type": "errand.accepted",
		"occurredAt": "2026-11-09T14:00:00Z", "errandId": errandID, "errandVersion": 3,
		"data": data,
	}
	if change != nil {
		change(env, data)
	}
	body, err := json.Marshal(env)
	require.NoError(t, err)
	return body
}

func TestDecodeAccepted(t *testing.T) {
	errandID := uuid.NewString()
	event, err := decodeAccepted(RoutingErrandAccepted, acceptedBody(t, errandID, nil))
	require.NoError(t, err)
	require.Equal(t, chat.AcceptedEvent{
		ErrandID: uuid.MustParse(errandID), ErrandVersion: 3,
		RequesterID: "auth0|requester", CourierID: "auth0|courier",
		OccurredAt: time.Date(2026, 11, 9, 14, 0, 0, 0, time.UTC),
	}, event)

	_, err = decodeAccepted(RoutingErrandAccepted, acceptedBody(t, errandID, func(env, data map[string]any) {
		env["futureField"] = "ignored"
		data["editDropped"] = true
	}))
	require.NoError(t, err, "fields chat-service does not use are ignored")
}

func TestDecodeAcceptedRejectsBrokenMessages(t *testing.T) {
	errandID := uuid.NewString()
	cases := map[string]func(env, data map[string]any){
		"no messageId":            func(env, _ map[string]any) { delete(env, "messageId") },
		"type is another event":   func(env, _ map[string]any) { env["type"] = "errand.cancelled" },
		"no occurredAt":           func(env, _ map[string]any) { delete(env, "occurredAt") },
		"occurredAt not a time":   func(env, _ map[string]any) { env["occurredAt"] = "yesterday" },
		"errandId not a UUID":     func(env, data map[string]any) { env["errandId"] = "42"; data["errandId"] = "42" },
		"version 0":               func(env, data map[string]any) { env["errandVersion"] = 0; data["errandVersion"] = 0 },
		"no data":                 func(env, _ map[string]any) { delete(env, "data") },
		"data not an object":      func(env, _ map[string]any) { env["data"] = "x" },
		"data for another errand": func(_, data map[string]any) { data["errandId"] = uuid.NewString() },
		"data version differs":    func(_, data map[string]any) { data["errandVersion"] = 4 },
		"no requesterId":          func(_, data map[string]any) { delete(data, "requesterId") },
		"no courierId":            func(_, data map[string]any) { delete(data, "courierId") },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := decodeAccepted(RoutingErrandAccepted, acceptedBody(t, errandID, change))
			require.ErrorIs(t, err, errMalformed)
		})
	}
	for name, body := range map[string][]byte{
		"not JSON":  []byte("{not json"),
		"empty":     nil,
		"too large": []byte(`{"pad":"` + strings.Repeat("a", maxBodyBytes) + `"}`),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := decodeAccepted(RoutingErrandAccepted, body)
			require.ErrorIs(t, err, errMalformed)
		})
	}
}

type fakeApplier struct {
	err    error
	events []chat.AcceptedEvent
}

func (f *fakeApplier) OnAccepted(_ context.Context, event chat.AcceptedEvent) (chat.AcceptResult, error) {
	f.events = append(f.events, event)
	if f.err != nil {
		return chat.AcceptResult{}, f.err
	}
	return chat.AcceptResult{Room: chat.Room{ID: uuid.New()}, Opened: true}, nil
}

func TestHandlerDispositions(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	body := acceptedBody(t, uuid.NewString(), nil)
	cases := []struct {
		name       string
		routingKey string
		body       []byte
		err        error
		want       Disposition
		applied    bool
	}{
		{"applied", RoutingErrandAccepted, body, nil, Ack, true},
		{"malformed is dead-lettered", RoutingErrandAccepted, []byte("nope"), nil, Reject, false},
		{"unknown routing key is dead-lettered", "errand.cancelled", body, nil, Reject, false},
		{"invalid event is dead-lettered", RoutingErrandAccepted, body, fmt.Errorf("%w: same person", chat.ErrInvalidEvent), Reject, true},
		{"database down is retried until it works", RoutingErrandAccepted, body, errors.Join(chat.ErrUnavailable, errors.New("down")), Retry, true},
		{"unexpected error is retried a few times", RoutingErrandAccepted, body, errors.New("surprise"), RetryLimited, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			applier := &fakeApplier{err: tc.err}
			got := NewHandler(applier, logger).Handle(context.Background(), tc.routingKey, "m-1", tc.body)
			require.Equal(t, tc.want, got)
			require.Equal(t, tc.applied, len(applier.events) == 1)
		})
	}
}

func TestBackoffDoublesUpToTheCap(t *testing.T) {
	b := Backoff{Base: time.Second, Cap: 5 * time.Second}
	require.Equal(t, []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 5 * time.Second, 5 * time.Second},
		[]time.Duration{b.Delay(1), b.Delay(2), b.Delay(3), b.Delay(4), b.Delay(100)})
}
