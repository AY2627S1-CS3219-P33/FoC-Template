package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/msgrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/stretchr/testify/require"
)

func testRow(kind string) msgrepo.Row {
	at := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	payload, _ := json.Marshal(errand.CreditCommand{ErrandID: "e-1", ErrandVersion: 2, RequesterID: "auth0|req1", Amount: 5})
	return msgrepo.Row{ID: 1, Kind: kind, IdempotencyKey: "e-1:reserve", ErrandID: "e-1", Payload: payload, Attempts: 1, CreatedAt: at}
}

func TestMessageFromOutboxPreservesOriginalMetadata(t *testing.T) {
	row := testRow(errand.KindCreditReserve)
	m, err := MessageFromOutbox(row)
	require.NoError(t, err)
	require.Equal(t, errand.ExchangeCommands, m.Exchange)
	require.Equal(t, row.IdempotencyKey, m.Envelope.MessageID)
	require.Equal(t, row.CreatedAt, m.Envelope.OccurredAt)
	require.Equal(t, 2, m.Envelope.ErrandVersion)
	require.JSONEq(t, string(row.Payload), string(m.Envelope.Data))

	at := row.CreatedAt.Add(-time.Minute)
	row.Kind = errand.ErrandOpened.Kind()
	row.Payload, _ = json.Marshal(errand.EventData{ErrandID: row.ErrandID, ErrandVersion: 2, Event: errand.EventReservationConfirmed, To: errand.StatusOpen, ActorType: errand.ActorSystem, RequesterID: "auth0|req1", Reward: 5, At: at})
	m, err = MessageFromOutbox(row)
	require.NoError(t, err)
	require.Equal(t, errand.ExchangeEvents, m.Exchange)
	require.Equal(t, string(errand.ErrandOpened), m.RoutingKey)
	require.Equal(t, at, m.Envelope.OccurredAt)
}

func TestInvalidOutboxRowsAreUnpublishable(t *testing.T) {
	for _, payload := range []string{`{`, `null`, `{"errandId":"other","errandVersion":2}`, `{"errandId":"e-1","errandVersion":0}`} {
		row := testRow(errand.KindCreditReserve)
		row.Payload = []byte(payload)
		_, err := MessageFromOutbox(row)
		require.ErrorIs(t, err, errand.ErrUnpublishable)
	}
	row := testRow("event.errand.unknown")
	_, err := MessageFromOutbox(row)
	require.ErrorIs(t, err, errand.ErrUnpublishable)
	row = testRow(errand.ErrandOpened.Kind())
	_, err = MessageFromOutbox(row)
	require.ErrorIs(t, err, errand.ErrUnpublishable, "event time is required")
}

type stubOutbox struct {
	rows                  []msgrepo.Row
	sent, failed, retried int
	code                  string
	delay                 time.Duration
}

func (s *stubOutbox) Claim(context.Context, string, bool, int, time.Duration) ([]msgrepo.Row, error) {
	return s.rows, nil
}
func (s *stubOutbox) MarkSent(context.Context, msgrepo.Row) error { s.sent++; return nil }
func (s *stubOutbox) Reschedule(_ context.Context, _ msgrepo.Row, delay time.Duration, code string) error {
	s.retried++
	s.delay = delay
	s.code = code
	return nil
}
func (s *stubOutbox) MarkFailed(_ context.Context, _ msgrepo.Row, code string) error {
	s.failed++
	s.code = code
	return nil
}
func (s *stubOutbox) RequeueUnconfirmed(context.Context, time.Duration) error { return nil }

type stubPublisher struct {
	err   error
	calls int
}

func (p *stubPublisher) Publish(context.Context, errand.Message) error { p.calls++; return p.err }

type stubSettings struct{}

func (stubSettings) Get(context.Context) (errand.SystemSettings, error) {
	return errand.DefaultSystemSettings(), nil
}

func TestDispatcherDeliveryOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		failure               error
		attempts              int32
		sent, retried, failed int
		code                  string
	}{
		{name: "confirmed by broker", attempts: 1, sent: 1},
		{name: "temporary failure", failure: errors.New("connection lost"), attempts: 1, retried: 1, code: "PUBLISH_FAILED"},
		{name: "unroutable", failure: ErrUnroutable, attempts: 1, retried: 1, code: "UNROUTABLE"},
		{name: "permanent", failure: errand.ErrUnpublishable, attempts: 1, failed: 1, code: "UNPUBLISHABLE"},
		{name: "last attempt", failure: errors.New("connection lost"), attempts: 10, failed: 1, code: "PUBLISH_FAILED"},
		{name: "crashed last attempt", attempts: 11, failed: 1, code: "ATTEMPTS_EXHAUSTED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := testRow(errand.KindCreditReserve)
			row.Attempts = tc.attempts
			store := &stubOutbox{rows: []msgrepo.Row{row}}
			pub := &stubPublisher{err: tc.failure}
			d := NewDispatcher(store, pub, stubSettings{}, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
			worked, err := d.dispatch(context.Background(), true)
			require.NoError(t, err)
			require.True(t, worked)
			require.Equal(t, tc.sent, store.sent)
			require.Equal(t, tc.retried, store.retried)
			require.Equal(t, tc.failed, store.failed)
			require.Equal(t, tc.code, store.code)
			if tc.retried > 0 {
				require.GreaterOrEqual(t, store.delay, time.Duration(0))
				require.LessOrEqual(t, store.delay, 2*time.Second)
			}
			if tc.attempts > 10 {
				require.Zero(t, pub.calls)
			}
		})
	}
}

func TestDispatcherCancellationLeavesLeaseForRecovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := &stubOutbox{rows: []msgrepo.Row{testRow(errand.KindCreditReserve)}}
	d := NewDispatcher(store, &stubPublisher{err: ctx.Err()}, stubSettings{}, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	_, err := d.dispatch(ctx, true)
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, store.sent+store.failed+store.retried)
	require.ErrorIs(t, d.Run(ctx), context.Canceled)
}

func TestPublisherRejectsInvalidCreditCommandsBeforeConnecting(t *testing.T) {
	p, err := NewPublisher("amqp://guest:guest@127.0.0.1:1/", false)
	require.NoError(t, err)
	defer p.Close()
	for _, command := range []errand.CreditCommand{
		{ErrandID: "e-1", ErrandVersion: 2, RequesterID: "auth0|req1", Amount: 0},
		{ErrandID: "other", ErrandVersion: 2, RequesterID: "auth0|req1", Amount: 5},
		{ErrandID: "e-1", ErrandVersion: 2, Amount: 5},
	} {
		row := testRow(errand.KindCreditReserve)
		row.Payload, _ = json.Marshal(command)
		_, err := MessageFromOutbox(row)
		require.ErrorIs(t, err, errand.ErrUnpublishable)
	}
	m, err := MessageFromOutbox(testRow(errand.KindCreditReserve))
	require.NoError(t, err)
	m.Envelope.MessageID = string(make([]byte, 256))
	require.ErrorIs(t, p.Publish(context.Background(), m), errand.ErrUnpublishable)
}
