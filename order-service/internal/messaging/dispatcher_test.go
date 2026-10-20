package messaging

import (
	"context"
	"encoding/json"
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
