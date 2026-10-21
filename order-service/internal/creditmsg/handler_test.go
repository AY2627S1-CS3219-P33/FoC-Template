package creditmsg

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/msgrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/testfakes"
	"github.com/stretchr/testify/require"
)

const (
	errandID    = "6f1c2b0e-8d4a-4e1b-9c3f-2a7d5e8b9c10"
	requesterID = "auth0|req1"
	courierID   = "auth0|cou1"
	reward      = 5
)

var (
	t0      = time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	discard = slog.New(slog.NewTextHandler(io.Discard, nil))
)

// fakeOutbox keeps the status of credit rows by idempotency key, with the
// same transitions as msgrepo's ConfirmByKey and MarkFailedByKey.
type fakeOutbox struct {
	mu     sync.Mutex
	status map[string]string
	codes  map[string]string
	fail   map[string][]error
}

func newFakeOutbox() *fakeOutbox {
	return &fakeOutbox{status: map[string]string{}, codes: map[string]string{}, fail: map[string][]error{}}
}

func (o *fakeOutbox) put(key, status string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.status[key]; !ok {
		o.status[key] = status
	}
}

func (o *fakeOutbox) Status(key string) string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.status[key]
}

func (o *fakeOutbox) failNext(method string, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.fail[method] = append(o.fail[method], err)
}

func (o *fakeOutbox) fault(method string) error {
	if q := o.fail[method]; len(q) > 0 {
		o.fail[method] = q[1:]
		return q[0]
	}
	return nil
}

func (o *fakeOutbox) ConfirmByKey(_ context.Context, key string) (bool, error) {
	return o.settle("ConfirmByKey", key, msgrepo.StatusConfirmed, "")
}

func (o *fakeOutbox) MarkFailedByKey(_ context.Context, key, code string) (bool, error) {
	return o.settle("MarkFailedByKey", key, msgrepo.StatusFailed, code)
}

func (o *fakeOutbox) settle(method, key, to, code string) (bool, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.fault(method); err != nil {
		return false, err
	}
	switch o.status[key] {
	case msgrepo.StatusPending, msgrepo.StatusSent:
		o.status[key], o.codes[key] = to, code
		return true, nil
	}
	return false, nil
}

// harness wires the handler to a fake store, a fake outbox and
// FakeCreditService. send plays the dispatcher and credit-service: it
// publishes the store's new credit rows, marks them SENT and returns
// credit-service's results for deliver.
type harness struct {
	t        *testing.T
	ctx      context.Context
	clock    *testfakes.FakeClock
	store    *testfakes.FakeStore
	credit   *testfakes.FakeCreditService
	outbox   *fakeOutbox
	counters *Counters
	handler  *Handler
	sent     int
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		t:        t,
		ctx:      context.Background(),
		clock:    testfakes.NewFakeClock(t0),
		store:    testfakes.NewFakeStore(),
		outbox:   newFakeOutbox(),
		counters: NewCounters(),
	}
	h.credit = testfakes.NewFakeCreditService(h.clock)
	h.credit.SetBalance(requesterID, 100)
	handler, err := NewHandler(h.store, h.outbox, h.clock, discard, h.counters)
	require.NoError(t, err)
	h.handler = handler
	return h
}

func details(reward int, now time.Time) errand.Details {
	return errand.Details{
		SupplierID:               "supplier-1",
		SupplierVersionID:        "supplier-version-1",
		DeliveryBuildingID:       "building-1",
		DeliveryBuildingSnapshot: json.RawMessage(`{"name":"COM1"}`),
		DeliveryDetail:           "Level 2 lounge",
		Items:                    json.RawMessage(`[{"description":"iced latte"}]`),
		ContactName:              "Alex Tan",
		ContactMobile:            "+65 9123 4567",
		Reward:                   reward,
		AcceptanceDeadline:       now.Add(time.Hour),
	}
}

// submit creates a PENDING_RESERVATION errand.
func (h *harness) submit() {
	h.t.Helper()
	e := errand.Errand{ID: errandID, RequesterID: requesterID, Details: details(reward, h.clock.Now()), CreateIdempotencyKey: "create-1"}
	_, err := h.store.Submit(h.ctx, e, h.clock.Now())
	require.NoError(h.t, err)
}

// open creates an errand and confirms its reservation through the handler.
func (h *harness) open() {
	h.t.Helper()
	h.submit()
	h.deliverAll(h.send(), Ack)
	require.Equal(h.t, errand.StatusOpen, h.errand().Status)
}

func (h *harness) fire(ev errand.Event, actor errand.Actor, input errand.EventInput) {
	h.t.Helper()
	_, _, err := h.store.Fire(h.ctx, errandID, ev, actor, input, h.clock.Now())
	require.NoError(h.t, err)
}

func (h *harness) editReward(newReward int) {
	h.t.Helper()
	d := details(newReward, h.clock.Now())
	d.AcceptanceDeadline = h.errand().Details.AcceptanceDeadline
	h.fire(errand.EventEdit, errand.Actor{Type: errand.ActorRequester, ID: requesterID}, errand.EventInput{Edit: &d})
	require.NotNil(h.t, h.errand().PendingEdit)
}

func (h *harness) accept() {
	h.t.Helper()
	h.fire(errand.EventAccept, errand.Actor{Type: errand.ActorCourier, ID: courierID}, errand.EventInput{Deadlines: errand.DefaultDeadlineSettings()})
}

func (h *harness) send() []errand.Message {
	h.t.Helper()
	rows := h.store.Outbox()
	var results []errand.Message
	for _, r := range rows[h.sent:] {
		if !strings.HasPrefix(r.Kind, "credit.") {
			continue
		}
		h.outbox.put(r.IdempotencyKey, msgrepo.StatusSent)
		m, err := testfakes.MessageFromOutbox(r)
		require.NoError(h.t, err)
		out, err := h.credit.Handle(m)
		require.NoError(h.t, err)
		results = append(results, out...)
	}
	h.sent = len(rows)
	return results
}

func (h *harness) deliver(m errand.Message) Disposition {
	h.t.Helper()
	body, err := json.Marshal(m.Envelope)
	require.NoError(h.t, err)
	return h.handler.Handle(h.ctx, m.RoutingKey, body)
}

func (h *harness) deliverAll(ms []errand.Message, want ...Disposition) {
	h.t.Helper()
	require.Len(h.t, ms, len(want))
	for i, m := range ms {
		require.Equal(h.t, want[i], h.deliver(m), "delivery %d (%s)", i, m.RoutingKey)
	}
}

func (h *harness) errand() errand.Errand {
	h.t.Helper()
	e, ok := h.store.Errand(errandID)
	require.True(h.t, ok)
	return e
}

func (h *harness) lastTransition() testfakes.TransitionRecord {
	ts := h.store.Transitions()
	return ts[len(ts)-1]
}

func key(op string, version int) string { return errand.IdempotencyKey(errandID, op, version) }

func TestReservedOpensErrand(t *testing.T) {
	h := newHarness(t)
	h.submit()
	results := h.send()
	require.Len(t, results, 1)
	require.Equal(t, errand.ResultCreditReserved, results[0].RoutingKey)

	require.Equal(t, Ack, h.deliver(results[0]))
	e := h.errand()
	require.Equal(t, errand.StatusOpen, e.Status)
	require.Equal(t, 2, e.Version)
	tr := h.lastTransition()
	require.Equal(t, errand.EventReservationConfirmed, tr.Event)
	require.Equal(t, errand.SystemActor(), tr.Actor)
	require.Equal(t, msgrepo.StatusConfirmed, h.outbox.Status(key("reserve", 0)))
	require.EqualValues(t, 1, h.counters.Count(errand.ResultCreditReserved, OutcomeApplied))
}

func TestReservationRefusedRejectsErrand(t *testing.T) {
	h := newHarness(t)
	h.credit.SetBalance(requesterID, reward-1)
	h.submit()
	results := h.send()
	require.Len(t, results, 1)
	require.Equal(t, errand.ResultCreditReservationRefused, results[0].RoutingKey)

	require.Equal(t, Ack, h.deliver(results[0]))
	require.Equal(t, errand.StatusRejected, h.errand().Status)
	require.Equal(t, errand.CreditCodeInsufficientCredits, h.lastTransition().Reason)
	require.Equal(t, msgrepo.StatusConfirmed, h.outbox.Status(key("reserve", 0)))
}

func TestReservationRefusedKeepsCreditCodeAsReason(t *testing.T) {
	h := newHarness(t)
	h.credit.Script(errand.CommandCreditReserve, testfakes.CreditScript{Outcome: testfakes.CreditRefuse, Code: errand.CreditCodeAccountNotFound})
	h.submit()
	h.deliverAll(h.send(), Ack)
	require.Equal(t, errand.StatusRejected, h.errand().Status)
	require.Equal(t, errand.CreditCodeAccountNotFound, h.lastTransition().Reason)
}

func TestAdjustedWithMatchingKeyAppliesEdit(t *testing.T) {
	h := newHarness(t)
	h.open()
	h.editReward(8)
	results := h.send()
	require.Len(t, results, 1)
	require.Equal(t, errand.ResultCreditAdjusted, results[0].RoutingKey)

	require.Equal(t, Ack, h.deliver(results[0]))
	e := h.errand()
	require.Nil(t, e.PendingEdit)
	require.Equal(t, 8, e.Details.Reward)
	require.Equal(t, errand.EventEditConfirmed, h.lastTransition().Event)
	require.Equal(t, msgrepo.StatusConfirmed, h.outbox.Status(key("adjust", 2)))
	require.EqualValues(t, 1, h.counters.Count(errand.ResultCreditAdjusted, OutcomeApplied))
}

func TestAdjustedAfterAcceptClearedEditIsIgnored(t *testing.T) {
	h := newHarness(t)
	h.open()
	h.editReward(8)
	results := h.send() // credit-service applied the adjust; its result is in flight
	require.Len(t, results, 1)
	h.accept() // drops the pending edit and sends a reverting adjust
	before := h.errand()
	transitions := len(h.store.Transitions())

	require.Equal(t, Ack, h.deliver(results[0]))
	after := h.errand()
	require.Equal(t, before, after, "errand unchanged")
	require.Equal(t, errand.StatusAssigned, after.Status)
	require.Equal(t, reward, after.Details.Reward)
	require.Len(t, h.store.Transitions(), transitions)
	require.Equal(t, msgrepo.StatusConfirmed, h.outbox.Status(key("adjust", 2)), "the command was answered")
	require.EqualValues(t, 1, h.counters.Count(errand.ResultCreditAdjusted, OutcomeResolved))

	// The revert's own result is ignored the same way and confirms its row.
	revert := h.send()
	require.Len(t, revert, 1)
	require.Equal(t, Ack, h.deliver(revert[0]))
	require.Equal(t, msgrepo.StatusConfirmed, h.outbox.Status(revert[0].Envelope.CorrelationID))
	reservation, ok := h.credit.Reservation(errandID)
	require.True(t, ok)
	require.Equal(t, reward, reservation.Amount, "the revert restored the original amount")
}

func TestAdjustRefusedRejectsEdit(t *testing.T) {
	h := newHarness(t)
	h.open()
	h.credit.SetBalance(requesterID, 0)
	h.editReward(50)
	results := h.send()
	require.Len(t, results, 1)
	require.Equal(t, errand.ResultCreditAdjustRefused, results[0].RoutingKey)

	require.Equal(t, Ack, h.deliver(results[0]))
	e := h.errand()
	require.Equal(t, errand.StatusOpen, e.Status)
	require.Nil(t, e.PendingEdit)
	require.Equal(t, reward, e.Details.Reward)
	tr := h.lastTransition()
	require.Equal(t, errand.EventEditRefused, tr.Event)
	require.Equal(t, errand.CreditCodeInsufficientCredits, tr.Reason)
	require.Equal(t, msgrepo.StatusConfirmed, h.outbox.Status(key("adjust", 2)))
}

func TestReleasedConfirmsRow(t *testing.T) {
	h := newHarness(t)
	h.open()
	h.fire(errand.EventCancel, errand.Actor{Type: errand.ActorRequester, ID: requesterID}, errand.EventInput{})
	results := h.send()
	require.Len(t, results, 1)
	require.Equal(t, errand.ResultCreditReleased, results[0].RoutingKey)
	transitions := len(h.store.Transitions())

	require.Equal(t, Ack, h.deliver(results[0]))
	require.Equal(t, msgrepo.StatusConfirmed, h.outbox.Status(key("release", 0)))
	require.Len(t, h.store.Transitions(), transitions, "no errand change")
	require.EqualValues(t, 1, h.counters.Count(errand.ResultCreditReleased, OutcomeApplied))
}

func TestTransferredConfirmsRow(t *testing.T) {
	h := newHarness(t)
	h.open()
	h.outbox.put(key("transfer", 0), msgrepo.StatusSent)
	m := resultMessage(errand.ResultCreditTransferred, key("transfer", 0), `{}`)
	require.Equal(t, Ack, h.deliver(m))
	require.Equal(t, msgrepo.StatusConfirmed, h.outbox.Status(key("transfer", 0)))
}

func TestOperationFailedMarksRowFailed(t *testing.T) {
	h := newHarness(t)
	h.open()
	h.credit.Script(errand.CommandCreditRelease, testfakes.CreditScript{
		Outcome: testfakes.CreditOperationFailed, Code: errand.CreditCodeReservationAlreadyTransferred,
	})
	h.fire(errand.EventCancel, errand.Actor{Type: errand.ActorRequester, ID: requesterID}, errand.EventInput{})
	results := h.send()
	require.Len(t, results, 1)
	require.Equal(t, errand.ResultCreditOperationFailed, results[0].RoutingKey)

	require.Equal(t, Ack, h.deliver(results[0]))
	require.Equal(t, msgrepo.StatusFailed, h.outbox.Status(key("release", 0)))
	require.Equal(t, errand.CreditCodeReservationAlreadyTransferred, h.outbox.codes[key("release", 0)])
	require.Equal(t, errand.StatusCancelled, h.errand().Status, "errand untouched")

	// Repeated: the row stays FAILED and nothing else happens.
	require.Equal(t, Ack, h.deliver(results[0]))
	require.EqualValues(t, 1, h.counters.Count(errand.ResultCreditOperationFailed, OutcomeApplied))
	require.EqualValues(t, 1, h.counters.Count(errand.ResultCreditOperationFailed, OutcomeResolved))
}

func TestDuplicateResultChangesNothingTheSecondTime(t *testing.T) {
	t.Run("reserved", func(t *testing.T) {
		h := newHarness(t)
		h.credit.Script(errand.CommandCreditReserve, testfakes.CreditScript{Outcome: testfakes.CreditDuplicateReply})
		h.submit()
		results := h.send()
		require.Len(t, results, 2)
		require.Equal(t, Ack, h.deliver(results[0]))
		after := h.errand()
		transitions := len(h.store.Transitions())

		require.Equal(t, Ack, h.deliver(results[1]))
		require.Equal(t, after, h.errand())
		require.Len(t, h.store.Transitions(), transitions)
		require.Len(t, h.store.OutboxKinds(errand.ErrandOpened.Kind()), 1, "one errand.opened event")
		require.EqualValues(t, 1, h.counters.Count(errand.ResultCreditReserved, OutcomeApplied))
		require.EqualValues(t, 1, h.counters.Count(errand.ResultCreditReserved, OutcomeResolved))
	})
	t.Run("refused", func(t *testing.T) {
		h := newHarness(t)
		h.credit.Script(errand.CommandCreditReserve, testfakes.CreditScript{Outcome: testfakes.CreditRefuse})
		h.submit()
		results := h.send()
		require.Equal(t, Ack, h.deliver(results[0]))
		transitions := len(h.store.Transitions())
		require.Equal(t, Ack, h.deliver(results[0]))
		require.Len(t, h.store.Transitions(), transitions)
		require.Equal(t, errand.StatusRejected, h.errand().Status)
	})
	t.Run("adjusted", func(t *testing.T) {
		h := newHarness(t)
		h.open()
		h.editReward(8)
		results := h.send()
		require.Equal(t, Ack, h.deliver(results[0]))
		after := h.errand()
		require.Equal(t, Ack, h.deliver(results[0]))
		require.Equal(t, after, h.errand())
		require.EqualValues(t, 1, h.counters.Count(errand.ResultCreditAdjusted, OutcomeResolved))
	})
	t.Run("released", func(t *testing.T) {
		h := newHarness(t)
		h.open()
		h.fire(errand.EventCancel, errand.Actor{Type: errand.ActorRequester, ID: requesterID}, errand.EventInput{})
		results := h.send()
		require.Equal(t, Ack, h.deliver(results[0]))
		require.Equal(t, Ack, h.deliver(results[0]))
		require.Equal(t, msgrepo.StatusConfirmed, h.outbox.Status(key("release", 0)))
		require.EqualValues(t, 1, h.counters.Count(errand.ResultCreditReleased, OutcomeResolved))
	})
}

func TestReservedAfterReconcilerRejectedIsResolved(t *testing.T) {
	h := newHarness(t)
	h.submit()
	results := h.send()
	h.fire(errand.EventReservationFailed, errand.SystemActor(), errand.EventInput{})
	require.Equal(t, errand.StatusRejected, h.errand().Status)

	require.Equal(t, Ack, h.deliver(results[0]))
	require.Equal(t, errand.StatusRejected, h.errand().Status)
	require.Equal(t, msgrepo.StatusConfirmed, h.outbox.Status(key("reserve", 0)))
	require.EqualValues(t, 1, h.counters.Count(errand.ResultCreditReserved, OutcomeResolved))
}

func TestApplyConflictIsDecidedAgain(t *testing.T) {
	h := newHarness(t)
	h.submit()
	results := h.send()
	h.store.FailNext("Apply", &apperror.Error{Code: apperror.ErrandChanged, Message: "stale"})

	require.Equal(t, Ack, h.deliver(results[0]))
	require.Equal(t, errand.StatusOpen, h.errand().Status)
	require.EqualValues(t, 1, h.counters.Count(errand.ResultCreditReserved, OutcomeApplied))
}

func TestTransientFailuresRequeue(t *testing.T) {
	down := errors.New("database unavailable")

	t.Run("reading the errand", func(t *testing.T) {
		h := newHarness(t)
		h.submit()
		results := h.send()
		h.store.FailNext("Get", down)
		require.Equal(t, Requeue, h.deliver(results[0]))
		require.Equal(t, errand.StatusPendingReservation, h.errand().Status)
		require.Equal(t, msgrepo.StatusSent, h.outbox.Status(key("reserve", 0)))

		require.Equal(t, Ack, h.deliver(results[0]))
		require.Equal(t, errand.StatusOpen, h.errand().Status)
		require.EqualValues(t, 1, h.counters.Count(errand.ResultCreditReserved, OutcomeRetried))
	})
	t.Run("applying", func(t *testing.T) {
		h := newHarness(t)
		h.submit()
		results := h.send()
		h.store.FailNext("Apply", down)
		require.Equal(t, Requeue, h.deliver(results[0]))
		require.Equal(t, errand.StatusPendingReservation, h.errand().Status)
	})
	t.Run("confirming after the transition committed", func(t *testing.T) {
		h := newHarness(t)
		h.submit()
		results := h.send()
		h.outbox.failNext("ConfirmByKey", down)
		require.Equal(t, Requeue, h.deliver(results[0]))
		require.Equal(t, errand.StatusOpen, h.errand().Status)
		require.Equal(t, msgrepo.StatusSent, h.outbox.Status(key("reserve", 0)))

		// The redelivery finds the transition applied and confirms the row.
		transitions := len(h.store.Transitions())
		require.Equal(t, Ack, h.deliver(results[0]))
		require.Len(t, h.store.Transitions(), transitions)
		require.Equal(t, msgrepo.StatusConfirmed, h.outbox.Status(key("reserve", 0)))
	})
	t.Run("marking failed", func(t *testing.T) {
		h := newHarness(t)
		h.outbox.put(key("release", 0), msgrepo.StatusSent)
		h.outbox.failNext("MarkFailedByKey", down)
		m := resultMessage(errand.ResultCreditOperationFailed, key("release", 0), `{"code":"RESERVATION_ALREADY_TRANSFERRED"}`)
		require.Equal(t, Requeue, h.deliver(m))
		require.Equal(t, msgrepo.StatusSent, h.outbox.Status(key("release", 0)))
	})
}

func resultMessage(resultType, correlationID, data string) errand.Message {
	return errand.Message{
		Exchange:   errand.ExchangeEvents,
		RoutingKey: resultType,
		Envelope: errand.Envelope{
			MessageID:     correlationID + ":" + resultType,
			CorrelationID: correlationID,
			Type:          resultType,
			OccurredAt:    t0,
			ErrandID:      errandID,
			ErrandVersion: 1,
			Data:          json.RawMessage(data),
		},
	}
}

func TestPoisonMessagesAreRejected(t *testing.T) {
	valid := resultMessage(errand.ResultCreditReserved, key("reserve", 0), `{"amount":5}`)
	body := func(m errand.Message) []byte {
		b, err := json.Marshal(m.Envelope)
		require.NoError(t, err)
		return b
	}
	with := func(edit func(*errand.Message)) []byte {
		m := valid
		edit(&m)
		return body(m)
	}
	cases := []struct {
		name       string
		routingKey string
		body       []byte
	}{
		{"unknown routing key", "credit.bogus", body(valid)},
		{"not JSON", errand.ResultCreditReserved, []byte("not json")},
		{"type differs from routing key", errand.ResultCreditReserved, with(func(m *errand.Message) { m.Envelope.Type = errand.ResultCreditReleased })},
		{"no correlationId", errand.ResultCreditReserved, with(func(m *errand.Message) { m.Envelope.CorrelationID = "" })},
		{"correlationId of another errand", errand.ResultCreditReserved, with(func(m *errand.Message) { m.Envelope.CorrelationID = "other:reserve" })},
		{"correlationId of another command", errand.ResultCreditReserved, with(func(m *errand.Message) { m.Envelope.CorrelationID = key("release", 0) })},
		{"undecodable data", errand.ResultCreditReserved, with(func(m *errand.Message) { m.Envelope.Data = json.RawMessage(`{"amount":"five"}`) })},
		{"refusal without code", errand.ResultCreditReservationRefused, with(func(m *errand.Message) {
			m.RoutingKey, m.Envelope.Type, m.Envelope.Data = errand.ResultCreditReservationRefused, errand.ResultCreditReservationRefused, json.RawMessage(`{}`)
		})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.submit()
			h.outbox.put(key("reserve", 0), msgrepo.StatusSent)
			require.Equal(t, Reject, h.handler.Handle(h.ctx, tc.routingKey, tc.body))
			require.Equal(t, errand.StatusPendingReservation, h.errand().Status)
			require.Equal(t, msgrepo.StatusSent, h.outbox.Status(key("reserve", 0)))
		})
	}

	t.Run("unknown result types share one counter label", func(t *testing.T) {
		h := newHarness(t)
		h.handler.Handle(h.ctx, "credit.bogus", body(valid))
		require.EqualValues(t, 1, h.counters.Count(ResultUnknown, OutcomeRejected))
	})
	t.Run("errand not found", func(t *testing.T) {
		h := newHarness(t)
		require.Equal(t, Reject, h.deliver(valid))
		require.EqualValues(t, 1, h.counters.Count(errand.ResultCreditReserved, OutcomeRejected))
	})
}

func TestNewHandlerRequiresDependencies(t *testing.T) {
	_, err := NewHandler(nil, newFakeOutbox(), testfakes.NewFakeClock(t0), discard, nil)
	require.Error(t, err)
	h, err := NewHandler(testfakes.NewFakeStore(), newFakeOutbox(), testfakes.NewFakeClock(t0), discard, nil)
	require.NoError(t, err)
	require.NotNil(t, h.Metrics())
}
