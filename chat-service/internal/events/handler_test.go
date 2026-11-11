// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-10
// Scope: Implementation of test code (unit and integration tests).
// Author review: Reviewed, validated, and tested by the authors.

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
	return eventBody(t, RoutingErrandAccepted, errandID, 3, "2026-11-09T14:00:00Z", change)
}

// eventTransitions is what order-service puts in data for each event chat
// reads: the transition's event name, from and to, and who acted.
var eventTransitions = map[string][4]string{
	RoutingErrandAccepted:  {"accept", "OPEN", "ASSIGNED", "COURIER"},
	RoutingErrandWithdrawn: {"withdraw", "ASSIGNED", "OPEN", "COURIER"},
	RoutingErrandCompleted: {"confirm", "DELIVERED", "COMPLETED", "REQUESTER"},
	RoutingErrandRefunded:  {"admin_refund", "REVIEW_REQUIRED", "REFUNDED", "ADMIN"},
}

// eventBody is an errand event as order-service publishes it. courierId is
// the errand's courier before the transition, so on errand.withdrawn it is
// the courier who withdrew.
func eventBody(t *testing.T, routingKey, errandID string, version int, at string, change func(env, data map[string]any)) []byte {
	t.Helper()
	transition := eventTransitions[routingKey]
	data := map[string]any{
		"errandId": errandID, "errandVersion": version, "event": transition[0],
		"from": transition[1], "to": transition[2], "actorType": transition[3], "actorId": "auth0|courier",
		"requesterId": "auth0|requester", "courierId": "auth0|courier",
		"reward": 5, "at": at,
	}
	env := map[string]any{
		"messageId": fmt.Sprintf("%s:%s:%d", errandID, routingKey, version), "type": routingKey,
		"occurredAt": at, "errandId": errandID, "errandVersion": version,
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

func TestDecodeWithdrawn(t *testing.T) {
	errandID := uuid.NewString()
	event, err := decodeWithdrawn(RoutingErrandWithdrawn,
		eventBody(t, RoutingErrandWithdrawn, errandID, 4, "2026-11-09T15:00:00Z", nil))
	require.NoError(t, err)
	require.Equal(t, chat.WithdrawnEvent{
		ErrandID: uuid.MustParse(errandID), ErrandVersion: 4,
		RequesterID: "auth0|requester", CourierID: "auth0|courier",
		OccurredAt: time.Date(2026, 11, 9, 15, 0, 0, 0, time.UTC),
	}, event)

	_, err = decodeWithdrawn(RoutingErrandWithdrawn,
		eventBody(t, RoutingErrandWithdrawn, errandID, 4, "2026-11-09T15:00:00Z", func(_, data map[string]any) {
			delete(data, "courierId")
		}))
	require.ErrorIs(t, err, errMalformed, "without courierId there is no room to end")

	_, err = decodeWithdrawn(RoutingErrandWithdrawn, acceptedBody(t, errandID, nil))
	require.ErrorIs(t, err, errMalformed, "the type must match the routing key")
}

func TestDecodeErrandEnded(t *testing.T) {
	errandID := uuid.NewString()
	for key, reason := range map[string]chat.EndReason{
		RoutingErrandCompleted: chat.EndCompleted,
		RoutingErrandRefunded:  chat.EndRefunded,
	} {
		t.Run(key, func(t *testing.T) {
			event, err := decodeErrandEnded(key, eventBody(t, key, errandID, 7, "2026-11-09T16:00:00Z", nil), reason)
			require.NoError(t, err)
			require.Equal(t, chat.ErrandEndedEvent{
				ErrandID: uuid.MustParse(errandID), ErrandVersion: 7, Reason: reason,
				OccurredAt: time.Date(2026, 11, 9, 16, 0, 0, 0, time.UTC),
			}, event)

			_, err = decodeErrandEnded(key, eventBody(t, key, errandID, 7, "2026-11-09T16:00:00Z", func(_, data map[string]any) {
				delete(data, "courierId")
			}), reason)
			require.NoError(t, err, "the courier is not needed: every room ends")

			_, err = decodeErrandEnded(key, eventBody(t, key, errandID, 7, "2026-11-09T16:00:00Z", func(env, _ map[string]any) {
				delete(env, "occurredAt")
			}), reason)
			require.ErrorIs(t, err, errMalformed, "the closing time comes from occurredAt")
		})
	}
}

// fakeApplier records which handler each event reached.
type fakeApplier struct {
	err   error
	calls []string
	ended []chat.ErrandEndedEvent
}

func (f *fakeApplier) OnAccepted(_ context.Context, _ chat.AcceptedEvent) (chat.AcceptResult, error) {
	f.calls = append(f.calls, "accepted")
	if f.err != nil {
		return chat.AcceptResult{}, f.err
	}
	return chat.AcceptResult{Room: chat.Room{ID: uuid.New()}, Opened: true}, nil
}

func (f *fakeApplier) OnWithdrawn(_ context.Context, _ chat.WithdrawnEvent) (chat.WithdrawResult, error) {
	f.calls = append(f.calls, "withdrawn")
	if f.err != nil {
		return chat.WithdrawResult{}, f.err
	}
	return chat.WithdrawResult{Room: chat.Room{ID: uuid.New()}, Closing: true}, nil
}

func (f *fakeApplier) OnErrandEnded(_ context.Context, event chat.ErrandEndedEvent) (chat.EndResult, error) {
	f.calls = append(f.calls, "ended")
	f.ended = append(f.ended, event)
	if f.err != nil {
		return chat.EndResult{}, f.err
	}
	return chat.EndResult{}, nil
}

func TestHandlerRoutesEachEvent(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	errandID := uuid.NewString()
	for key, want := range map[string]string{
		RoutingErrandAccepted:  "accepted",
		RoutingErrandWithdrawn: "withdrawn",
		RoutingErrandCompleted: "ended",
		RoutingErrandRefunded:  "ended",
	} {
		t.Run(key, func(t *testing.T) {
			applier := &fakeApplier{}
			body := eventBody(t, key, errandID, 3, "2026-11-09T14:00:00Z", nil)
			require.Equal(t, Ack, NewHandler(applier, logger).Handle(context.Background(), key, "m-1", body))
			require.Equal(t, []string{want}, applier.calls)
		})
	}

	applier := &fakeApplier{}
	handler := NewHandler(applier, logger)
	handler.Handle(context.Background(), RoutingErrandCompleted, "m-1",
		eventBody(t, RoutingErrandCompleted, errandID, 3, "2026-11-09T14:00:00Z", nil))
	handler.Handle(context.Background(), RoutingErrandRefunded, "m-2",
		eventBody(t, RoutingErrandRefunded, errandID, 3, "2026-11-09T14:00:00Z", nil))
	require.Equal(t, chat.EndCompleted, applier.ended[0].Reason)
	require.Equal(t, chat.EndRefunded, applier.ended[1].Reason)

	require.Equal(t, Reject, handler.Handle(context.Background(), RoutingErrandWithdrawn, "m-3",
		acceptedBody(t, errandID, nil)), "a body whose type is another event is dead-lettered")
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
			require.Equal(t, tc.applied, len(applier.calls) == 1)
		})
	}
}

func TestBackoffDoublesUpToTheCap(t *testing.T) {
	b := Backoff{Base: time.Second, Cap: 5 * time.Second}
	require.Equal(t, []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 5 * time.Second, 5 * time.Second},
		[]time.Duration{b.Delay(1), b.Delay(2), b.Delay(3), b.Delay(4), b.Delay(100)})
}
