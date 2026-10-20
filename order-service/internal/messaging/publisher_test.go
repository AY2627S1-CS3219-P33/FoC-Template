package messaging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/stretchr/testify/require"
)

func TestLogPublisherLogsIdentityButNotBody(t *testing.T) {
	var buf bytes.Buffer
	p := NewLogPublisher(slog.New(slog.NewJSONHandler(&buf, nil)))
	m, err := MessageFromOutbox(testRow(errand.KindCreditReserve))
	require.NoError(t, err)
	require.NoError(t, p.Publish(context.Background(), m))
	require.NoError(t, p.Ping(context.Background()))

	var line map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &line))
	require.Equal(t, errand.ExchangeCommands, line["exchange"])
	require.Equal(t, errand.CommandCreditReserve, line["routing_key"])
	require.Equal(t, m.Envelope.MessageID, line["message_id"])
	require.Equal(t, m.Envelope.ErrandID, line["errand_id"])
	require.NotContains(t, buf.String(), "auth0|req1", "user IDs in the body are never logged")

	m.Envelope.MessageID = ""
	require.ErrorIs(t, p.Publish(context.Background(), m), errand.ErrUnpublishable)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m.Envelope.MessageID = "k"
	require.ErrorIs(t, p.Publish(ctx, m), context.Canceled)
}

func TestPublisherBacksOffAfterFailedConnect(t *testing.T) {
	// Nothing listens on port 1, so the dial is refused at once.
	p, err := NewPublisher("amqp://guest:s3cret@127.0.0.1:1/", false)
	require.NoError(t, err)
	p.backoff.Rand = func(n int64) int64 { return n - 1 }
	m, err := MessageFromOutbox(testRow(errand.KindCreditReserve))
	require.NoError(t, err)

	err = p.Publish(context.Background(), m)
	require.ErrorIs(t, err, ErrBrokerUnavailable)
	require.NotErrorIs(t, err, errand.ErrUnpublishable, "a broker outage is retryable")
	require.NotContains(t, err.Error(), "s3cret")
	require.Equal(t, 1, p.failures)
	require.WithinDuration(t, time.Now().Add(p.backoff.Base), p.retryAt, 200*time.Millisecond)

	// Inside the backoff window nothing is dialled.
	err = p.Publish(context.Background(), m)
	require.ErrorIs(t, err, ErrBrokerUnavailable)
	require.Contains(t, err.Error(), "reconnecting in")
	require.Equal(t, 1, p.failures)
	require.ErrorIs(t, p.Ping(context.Background()), ErrBrokerUnavailable)

	p.retryAt = time.Time{}
	require.ErrorIs(t, p.Ping(context.Background()), ErrBrokerUnavailable)
	require.Equal(t, 2, p.failures, "the delay grows with consecutive failures")
	require.WithinDuration(t, time.Now().Add(2*p.backoff.Base), p.retryAt, 200*time.Millisecond)

	require.NoError(t, p.Close())
	require.ErrorIs(t, p.Publish(context.Background(), m), errClosed)
	require.ErrorIs(t, p.Ping(context.Background()), errClosed)
}
