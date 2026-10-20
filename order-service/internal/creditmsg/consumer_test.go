package creditmsg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/msgrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/messaging"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/resilience"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/require"
)

var fastConfig = Config{
	ReconnectBackoff: resilience.BackoffPolicy{Base: 10 * time.Millisecond, Cap: 100 * time.Millisecond},
	RetryBackoff:     resilience.BackoffPolicy{Base: 10 * time.Millisecond, Cap: 50 * time.Millisecond},
}

func TestConfigDefaultsAndValidation(t *testing.T) {
	cfg, err := Config{}.withDefaults()
	require.NoError(t, err)
	require.Equal(t, DefaultConfig().Prefetch, cfg.Prefetch)
	require.Equal(t, 16, cfg.Prefetch)
	require.Equal(t, DefaultConfig().HandleTimeout, cfg.HandleTimeout)

	for _, bad := range []Config{
		{Prefetch: -1},
		{Prefetch: 5000},
		{HandleTimeout: -time.Second},
		{ReconnectBackoff: resilience.BackoffPolicy{Base: time.Second, Cap: time.Millisecond}},
		{RetryBackoff: resilience.BackoffPolicy{Base: -time.Second}},
	} {
		_, err := bad.withDefaults()
		require.Error(t, err, "%+v", bad)
	}
}

func TestNewConsumerValidatesWithoutLeakingURL(t *testing.T) {
	h := newHarness(t)
	_, err := NewConsumer("", h.handler, discard, Config{})
	require.Error(t, err)
	_, err = NewConsumer("http://user:secret@host/", h.handler, discard, Config{})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "secret")
	_, err = NewConsumer("amqp://guest:guest@127.0.0.1:5672/", nil, discard, Config{})
	require.Error(t, err)
}

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestRunRetriesUnreachableBrokerWithoutLeakingURL(t *testing.T) {
	h := newHarness(t)
	var logs syncBuffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	// Port 1 refuses connections at once.
	c, err := NewConsumer("amqp://user:s3cret@127.0.0.1:1/", h.handler, logger, fastConfig)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, c.Run(ctx), context.DeadlineExceeded)
	require.False(t, c.Running())
	out := logs.String()
	require.GreaterOrEqual(t, strings.Count(out, "reconnecting"), 2, "retried with backoff")
	require.NotContains(t, out, "s3cret")
	require.NotContains(t, out, "user:")
}

func TestHandlerPanicRequeues(t *testing.T) {
	h := newHarness(t)
	c, err := NewConsumer("amqp://guest:guest@127.0.0.1:5672/", h.handler, discard, fastConfig)
	require.NoError(t, err)
	c.handler = &Handler{logger: discard, metrics: NewCounters()} // nil store: Handle panics
	m := resultMessage(errand.ResultCreditReserved, key("reserve", 0), `{"amount":5}`)
	body, err := json.Marshal(m.Envelope)
	require.NoError(t, err)
	require.Equal(t, Requeue, c.handle(context.Background(), amqp.Delivery{RoutingKey: m.RoutingKey, Body: body}))
}

// ---------------------------------------------------------------------------
// RabbitMQ (skipped without TEST_AMQP_URL)
// ---------------------------------------------------------------------------

// testBroker declares the topology and empties order-service's queues.
func testBroker(t *testing.T) (string, *amqp.Channel) {
	t.Helper()
	url := os.Getenv("TEST_AMQP_URL")
	if url == "" {
		t.Skip("TEST_AMQP_URL is not set")
	}
	conn, err := amqp.Dial(url)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.CloseDeadline(time.Now().Add(time.Second)) })
	ch, err := conn.Channel()
	require.NoError(t, err)
	require.NoError(t, messaging.DeclareTopology(ch, false))
	for _, q := range []string{errand.QueueCreditResults, errand.QueueCreditResultsDLQ} {
		_, err := ch.QueuePurge(q, false)
		require.NoError(t, err)
	}
	return url, ch
}

func startConsumer(t *testing.T, url string, h *harness) *Consumer {
	t.Helper()
	c, err := NewConsumer(url, h.handler, discard, fastConfig)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			require.ErrorIs(t, err, context.Canceled)
		case <-time.After(5 * time.Second):
			t.Error("consumer did not stop")
		}
	})
	require.Eventually(t, c.Running, 5*time.Second, 10*time.Millisecond)
	return c
}

func publish(t *testing.T, ch *amqp.Channel, routingKey string, body []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, ch.PublishWithContext(ctx, errand.ExchangeEvents, routingKey, false, false, amqp.Publishing{
		DeliveryMode: amqp.Persistent, ContentType: "application/json", Type: routingKey, Body: body,
	}))
}

func publishResult(t *testing.T, ch *amqp.Channel, m errand.Message) {
	t.Helper()
	body, err := json.Marshal(m.Envelope)
	require.NoError(t, err)
	publish(t, ch, m.RoutingKey, body)
}

func queueDepth(t *testing.T, ch *amqp.Channel, name string) int {
	t.Helper()
	q, err := ch.QueueDeclarePassive(name, true, false, false, false, nil)
	require.NoError(t, err)
	return q.Messages
}

func TestConsumerAppliesPublishedResult(t *testing.T) {
	url, ch := testBroker(t)
	h := newHarness(t)
	h.submit()
	results := h.send()
	require.Len(t, results, 1)
	c := startConsumer(t, url, h)

	publishResult(t, ch, results[0])
	require.Eventually(t, func() bool {
		return h.errand().Status == errand.StatusOpen && h.outbox.Status(key("reserve", 0)) == msgrepo.StatusConfirmed
	}, 5*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return queueDepth(t, ch, errand.QueueCreditResults) == 0 }, 5*time.Second, 20*time.Millisecond, "acked")
	require.EqualValues(t, 1, c.Metrics().(*Counters).Count(errand.ResultCreditReserved, OutcomeApplied))
	require.Zero(t, queueDepth(t, ch, errand.QueueCreditResultsDLQ))
}

func TestConsumerRequeuesTransientFailure(t *testing.T) {
	url, ch := testBroker(t)
	h := newHarness(t)
	h.submit()
	results := h.send()
	h.store.FailNext("Get", errors.New("database unavailable"), errors.New("database unavailable"))
	startConsumer(t, url, h)

	publishResult(t, ch, results[0])
	require.Eventually(t, func() bool { return h.errand().Status == errand.StatusOpen }, 5*time.Second, 10*time.Millisecond)
	require.EqualValues(t, 2, h.counters.Count(errand.ResultCreditReserved, OutcomeRetried))
	require.EqualValues(t, 1, h.counters.Count(errand.ResultCreditReserved, OutcomeApplied))
	require.Zero(t, queueDepth(t, ch, errand.QueueCreditResultsDLQ))
}

func TestPoisonMessageIsDeadLettered(t *testing.T) {
	url, ch := testBroker(t)
	h := newHarness(t)
	h.submit()
	startConsumer(t, url, h)

	valid, err := json.Marshal(resultMessage(errand.ResultCreditReserved, key("reserve", 0), `{"amount":5}`).Envelope)
	require.NoError(t, err)
	publish(t, ch, "credit.bogus", valid)                    // unknown routing key, still bound by credit.#
	publish(t, ch, errand.ResultCreditReserved, []byte("{")) // undecodable body

	require.Eventually(t, func() bool { return queueDepth(t, ch, errand.QueueCreditResultsDLQ) == 2 }, 5*time.Second, 20*time.Millisecond)
	require.Zero(t, queueDepth(t, ch, errand.QueueCreditResults))
	d, ok, err := ch.Get(errand.QueueCreditResultsDLQ, true)
	require.NoError(t, err)
	require.True(t, ok)
	// Dead-lettering rewrites the routing key to the DLQ's; Type keeps the original.
	require.Equal(t, "credit.bogus", d.Type)
	require.Equal(t, "rejected", d.Headers["x-first-death-reason"])
	require.Equal(t, errand.StatusPendingReservation, h.errand().Status, "nothing applied")
	require.EqualValues(t, 1, h.counters.Count(ResultUnknown, OutcomeRejected))
	require.EqualValues(t, 1, h.counters.Count(errand.ResultCreditReserved, OutcomeRejected))
}

func TestConsumerResumesAfterConnectionClosed(t *testing.T) {
	url, ch := testBroker(t)
	h := newHarness(t)
	h.submit()
	results := h.send()
	c := startConsumer(t, url, h)

	c.mu.Lock()
	first := c.conn
	c.mu.Unlock()
	require.NoError(t, first.Close())
	require.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.conn != nil && c.conn != first && c.Running()
	}, 5*time.Second, 10*time.Millisecond, "reconnected and subscribed again")

	publishResult(t, ch, results[0])
	require.Eventually(t, func() bool { return h.errand().Status == errand.StatusOpen }, 5*time.Second, 10*time.Millisecond)
}
