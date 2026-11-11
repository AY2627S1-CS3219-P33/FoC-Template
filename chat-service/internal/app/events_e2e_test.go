// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-10
// Scope: Implementation of test code (unit and integration tests).
// Author review: Reviewed, validated, and tested by the authors.

package app

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/events"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/testdb"
)

// TestAcceptedErrandOpensARoomEndToEnd is the phase 4 check in code: the real
// app runs with a real PostgreSQL and RabbitMQ, an errand.accepted event
// shaped exactly like order-service's is published, and the room appears.
func TestAcceptedErrandOpensARoomEndToEnd(t *testing.T) {
	amqpURL := os.Getenv("TEST_AMQP_URL")
	if amqpURL == "" {
		t.Skip("TEST_AMQP_URL is not set; skipping RabbitMQ test")
	}
	ctx := context.Background()
	db := testdb.Migrated(t)
	cfg := testConfig(db.URL)
	cfg.AMQP.URL = amqpURL
	cfg.HTTP.Port = freePort(t)
	application, err := New(ctx, cfg, discardLogger())
	require.NoError(t, err)
	t.Cleanup(application.Close)

	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- application.Run(runCtx) }()
	t.Cleanup(func() {
		stop()
		require.NoError(t, <-done)
	})

	readyz := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.HTTP.Port)) + "/readyz"
	require.Eventually(t, func() bool {
		response, err := http.Get(readyz)
		if err != nil {
			return false
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		return response.StatusCode == http.StatusOK && string(body) ==
			`{"status":"ready","dependencies":{"errandEvents":"ok","postgres":"ok"}}`+"\n"
	}, 15*time.Second, 50*time.Millisecond, "the consumer subscribes and /readyz reports it")

	conn, err := amqp.Dial(amqpURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	ch, err := conn.Channel()
	require.NoError(t, err)
	errandID := uuid.NewString()
	body, err := json.Marshal(map[string]any{
		"messageId": errandID + ":accepted:3", "type": "errand.accepted",
		"occurredAt": "2026-11-09T14:00:00Z", "errandId": errandID, "errandVersion": 3,
		"data": map[string]any{
			"errandId": errandID, "errandVersion": 3, "event": "accept", "from": "OPEN", "to": "ASSIGNED",
			"actorType": "COURIER", "actorId": "auth0|courier",
			"requesterId": "auth0|requester", "courierId": "auth0|courier",
			"reward": 5, "at": "2026-11-09T14:00:00Z",
		},
	})
	require.NoError(t, err)
	require.NoError(t, ch.PublishWithContext(ctx, events.Exchange, events.RoutingErrandAccepted, true, false, amqp.Publishing{
		ContentType: "application/json", DeliveryMode: amqp.Persistent,
		MessageId: errandID + ":accepted:3", Type: events.RoutingErrandAccepted, Body: body,
	}))

	require.Eventually(t, func() bool {
		var count int
		err := db.Pool.QueryRow(ctx,
			`SELECT count(*) FROM chat_rooms WHERE errand_id = $1 AND requester_id = 'auth0|requester'
			 AND courier_id = 'auth0|courier' AND opened_version = 3`, errandID).Scan(&count)
		return err == nil && count == 1
	}, 10*time.Second, 50*time.Millisecond, "the room appears")
}
