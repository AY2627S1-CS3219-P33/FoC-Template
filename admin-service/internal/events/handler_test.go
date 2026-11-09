package events

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/facts"
)

const errandID = "6f1c2b0e-8d4a-4e1b-9c3f-2a7d5e8b9c10"

type fakeRecorder struct {
	events  []facts.Event
	seen    map[string]bool
	err     error
	applied int
}

func (f *fakeRecorder) Apply(_ context.Context, e facts.Event) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	f.events = append(f.events, e)
	if f.seen == nil {
		f.seen = map[string]bool{}
	}
	if f.seen[e.MessageID] {
		return false, nil
	}
	f.seen[e.MessageID] = true
	f.applied++
	return true, nil
}

func newHandler(recorder Recorder, wakes *int) *Handler {
	return NewHandler(recorder, func() { *wakes++ }, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// body is an event envelope as order-service publishes it (docs/contracts.md).
func body(eventType string, version int, from, to string, extra string) []byte {
	fromField := ""
	if from != "" {
		fromField = fmt.Sprintf(`"from": %q,`, from)
	}
	return []byte(fmt.Sprintf(`{
		"messageId": %q, "type": %q, "occurredAt": "2026-10-14T09:12:30Z",
		"errandId": %q, "errandVersion": %d,
		"data": {"errandId": %q, "errandVersion": %d, "event": "x", %s "to": %q,
		         "actorType": "COURIER", "actorId": "auth0|cou1", "requesterId": "auth0|req1",
		         "courierId": "auth0|cou1", "reward": 8, "at": "2026-10-14T09:12:30Z" %s}
	}`, errandID+":"+eventType[len("errand."):]+fmt.Sprintf(":%d", version), eventType, errandID, version, errandID, version, fromField, to, extra))
}

func TestHandleRecordsATransitionEvent(t *testing.T) {
	recorder := &fakeRecorder{}
	wakes := 0

	d := newHandler(recorder, &wakes).Handle(context.Background(), "errand.picked_up", body("errand.picked_up", 5, "ASSIGNED", "PICKED_UP", ""))

	require.Equal(t, Ack, d)
	require.Len(t, recorder.events, 1)
	e := recorder.events[0]
	require.Equal(t, errandID+":picked_up:5", e.MessageID)
	require.Equal(t, "errand.picked_up", e.Type)
	require.Equal(t, errandID, e.ErrandID)
	require.Equal(t, 5, e.Version)
	require.Equal(t, facts.StatusAssigned, e.From)
	require.Equal(t, facts.StatusPickedUp, e.To)
	require.Equal(t, "auth0|cou1", e.CourierID)
	require.Equal(t, 8, e.Reward)
	require.Equal(t, "2026-10-14T09:12:30Z", e.OccurredAt.Format("2006-01-02T15:04:05Z07:00"))
	require.Zero(t, wakes, "a pickup owes no enrichment")
}

func TestHandleWakesEnrichmentAfterAcceptedAndTerminalEvents(t *testing.T) {
	for name, tc := range map[string]struct {
		eventType, from, to string
		wakes               int
	}{
		"accepted":   {"errand.accepted", "OPEN", "ASSIGNED", 1},
		"completed":  {"errand.completed", "DELIVERED", "COMPLETED", 1},
		"expired":    {"errand.expired", "ASSIGNED", "EXPIRED", 1},
		"cancelled":  {"errand.cancelled", "OPEN", "CANCELLED", 1},
		"rejected":   {"errand.rejected", "PENDING_RESERVATION", "REJECTED", 1},
		"refunded":   {"errand.refunded", "REVIEW_REQUIRED", "REFUNDED", 1},
		"opened":     {"errand.opened", "PENDING_RESERVATION", "OPEN", 0},
		"withdrawn":  {"errand.withdrawn", "ASSIGNED", "OPEN", 0},
		"delivered":  {"errand.delivered", "PICKED_UP", "DELIVERED", 0},
		"edited":     {"errand.edited", "OPEN", "OPEN", 0},
		"in review":  {"errand.review_required", "DELIVERED", "REVIEW_REQUIRED", 0},
		"edit asked": {"errand.edit_requested", "OPEN", "OPEN", 0},
	} {
		t.Run(name, func(t *testing.T) {
			wakes := 0
			d := newHandler(&fakeRecorder{}, &wakes).Handle(context.Background(), tc.eventType, body(tc.eventType, 4, tc.from, tc.to, ""))

			require.Equal(t, Ack, d)
			require.Equal(t, tc.wakes, wakes)
		})
	}
}

func TestHandleDuplicateDeliveryIsAckedWithoutWork(t *testing.T) {
	recorder := &fakeRecorder{}
	wakes := 0
	h := newHandler(recorder, &wakes)
	msg := body("errand.accepted", 3, "OPEN", "ASSIGNED", "")

	require.Equal(t, Ack, h.Handle(context.Background(), "errand.accepted", msg))
	require.Equal(t, Ack, h.Handle(context.Background(), "errand.accepted", msg))

	require.Len(t, recorder.events, 2, "Apply is asked twice; the store decides it is a duplicate")
	require.Equal(t, 1, recorder.applied)
	require.Equal(t, 1, wakes, "only the first delivery wakes enrichment")
}

func TestHandleIgnoresEventsTheReadModelDoesNotKeep(t *testing.T) {
	recorder := &fakeRecorder{}
	wakes := 0
	h := newHandler(recorder, &wakes)

	for _, key := range []string{
		"errand.accept_rejected", "errand.cancel_rejected", "errand.pickup_rejected",
		"errand.verification_reminder", "errand.pickup_deadline_approaching",
		"errand.something_new", "credit.reserved",
	} {
		require.Equal(t, Ack, h.Handle(context.Background(), key, []byte("not even json")), key)
	}
	require.Empty(t, recorder.events)
}

func TestHandleRejectsWhatCanNeverBeProcessed(t *testing.T) {
	recorder := &fakeRecorder{}
	wakes := 0
	h := newHandler(recorder, &wakes)
	good := body("errand.accepted", 3, "OPEN", "ASSIGNED", "")

	for name, tc := range map[string]struct {
		key  string
		body []byte
	}{
		"not json":              {"errand.accepted", []byte("<html>")},
		"empty body":            {"errand.accepted", nil},
		"a json array":          {"errand.accepted", []byte("[]")},
		"type differs from key": {"errand.opened", good},
		"no message id":         {"errand.accepted", []byte(`{"type":"errand.accepted","occurredAt":"2026-10-14T09:12:30Z","errandId":"` + errandID + `","errandVersion":3,"data":{"to":"ASSIGNED"}}`)},
		"unknown status":        {"errand.accepted", body("errand.accepted", 3, "OPEN", "LOST", "")},
		"no time":               {"errand.accepted", []byte(`{"messageId":"m","type":"errand.accepted","errandId":"` + errandID + `","errandVersion":3,"data":{"to":"ASSIGNED"}}`)},
		"bad errand id":         {"errand.accepted", []byte(`{"messageId":"m","type":"errand.accepted","occurredAt":"2026-10-14T09:12:30Z","errandId":"E","errandVersion":3,"data":{"to":"ASSIGNED"}}`)},
		"zero version":          {"errand.accepted", body("errand.accepted", 0, "OPEN", "ASSIGNED", "")},
		"completed not ending":  {"errand.completed", body("errand.completed", 8, "DELIVERED", "ASSIGNED", "")},
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, Reject, h.Handle(context.Background(), tc.key, tc.body))
		})
	}
	require.Empty(t, recorder.events, "nothing reaches the store")
}

func TestHandleToleratesUnknownFieldsInTheEnvelope(t *testing.T) {
	recorder := &fakeRecorder{}
	wakes := 0
	msg := body("errand.accepted", 3, "OPEN", "ASSIGNED", `, "editDropped": true, "somethingNew": {"a": 1}`)

	require.Equal(t, Ack, newHandler(recorder, &wakes).Handle(context.Background(), "errand.accepted", msg))
	require.Len(t, recorder.events, 1)
}

func TestHandleRequeuesATransientStoreFailureAndRejectsAnInvalidEvent(t *testing.T) {
	wakes := 0
	msg := body("errand.accepted", 3, "OPEN", "ASSIGNED", "")

	require.Equal(t, Requeue, newHandler(&fakeRecorder{err: errors.New("connection reset")}, &wakes).
		Handle(context.Background(), "errand.accepted", msg))
	require.Equal(t, Reject, newHandler(&fakeRecorder{err: fmt.Errorf("%w: refused", facts.ErrInvalid)}, &wakes).
		Handle(context.Background(), "errand.accepted", msg))
	require.Zero(t, wakes)
}

func TestHandleDoesNotNeedAnEnrichmentWaker(t *testing.T) {
	h := NewHandler(&fakeRecorder{}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	require.Equal(t, Ack, h.Handle(context.Background(), "errand.completed", body("errand.completed", 8, "DELIVERED", "COMPLETED", "")))
}

func TestDispositionString(t *testing.T) {
	require.Equal(t, "ack", Ack.String())
	require.Equal(t, "reject", Reject.String())
	require.Equal(t, "requeue", Requeue.String())
}
