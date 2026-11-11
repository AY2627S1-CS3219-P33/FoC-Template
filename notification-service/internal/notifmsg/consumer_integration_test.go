// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: Tests for notification-service notification event messaging.
// Author review: I validated correctness, edited for style, and added boundary checks.

package notifmsg_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/notification-service/internal/notification"
	"github.com/CS3219-AY2627S1/FoC-Template/notification-service/internal/notifmsg"
)

// These tests need a RabbitMQ: set TEST_AMQP_URL, for example
// amqp://guest:guest@127.0.0.1:5673/ after
// `docker compose -f compose.test.yaml up -d`. The tests delete and recreate
// notification-service's queues, so use a throwaway broker.

func brokerChannel(t *testing.T) *amqp.Channel {
	t.Helper()
	url := os.Getenv("TEST_AMQP_URL")
	if url == "" {
		t.Skip("TEST_AMQP_URL is not set")
	}
	conn, err := amqp.Dial(url)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	ch, err := conn.Channel()
	require.NoError(t, err)
	for _, queue := range []string{notifmsg.QueueErrands, notifmsg.QueueErrandsDLQ} {
		_, err := ch.QueueDelete(queue, false, false, false)
		require.NoError(t, err)
	}
	return ch
}

func testConfig() notifmsg.Config {
	return notifmsg.Config{
		RetryBackoff:     notifmsg.Backoff{Base: time.Millisecond, Cap: 5 * time.Millisecond},
		ReconnectBackoff: notifmsg.Backoff{Base: 10 * time.Millisecond, Cap: 50 * time.Millisecond},
	}
}

func startConsumer(t *testing.T, handler notifmsg.MessageHandler) {
	t.Helper()
	consumer, err := notifmsg.NewConsumer(os.Getenv("TEST_AMQP_URL"), handler,
		slog.New(slog.NewTextHandler(io.Discard, nil)), testConfig())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- consumer.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		require.ErrorIs(t, <-done, context.Canceled)
	})
	require.Eventually(t, consumer.Running, 10*time.Second, 10*time.Millisecond)
}

// event builds a well-formed errand event envelope with the given data.
func event(t *testing.T, routingKey string, data any) []byte {
	t.Helper()
	raw, err := json.Marshal(data)
	require.NoError(t, err)
	body, err := json.Marshal(map[string]any{
		"messageId":     "11111111-1111-1111-1111-111111111111:" + routingKey,
		"type":          routingKey,
		"occurredAt":    "2026-01-02T03:04:05Z",
		"errandId":      "11111111-1111-1111-1111-111111111111",
		"errandVersion": 1,
		"data":          json.RawMessage(raw),
	})
	require.NoError(t, err)
	return body
}

func publish(t *testing.T, ch *amqp.Channel, routingKey string, body []byte) {
	t.Helper()
	require.NoError(t, ch.PublishWithContext(context.Background(), notifmsg.ExchangeEvents, routingKey, true, false,
		amqp.Publishing{ContentType: "application/json", DeliveryMode: amqp.Persistent, Body: body}))
}

func queueLength(t *testing.T, ch *amqp.Channel, name string) int {
	t.Helper()
	q, err := ch.QueueInspect(name)
	require.NoError(t, err)
	return q.Messages
}

// recorder is an in-memory notifmsg.Recorder that captures what the handler
// would store, so the broker path is tested without a database.
type recorder struct {
	mu    sync.Mutex
	items []notification.Pending
}

func (r *recorder) Record(_ context.Context, _ string, items []notification.Pending) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items = append(r.items, items...)
	return len(items), nil
}

func (r *recorder) snapshot() []notification.Pending {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]notification.Pending(nil), r.items...)
}

type handlerFunc func(ctx context.Context, routingKey string, body []byte) error

func (f handlerFunc) Handle(ctx context.Context, routingKey string, body []byte) error {
	return f(ctx, routingKey, body)
}

func TestConsumerStoresANotificationFromAnEvent(t *testing.T) {
	ch := brokerChannel(t)
	rec := &recorder{}
	startConsumer(t, notifmsg.NewHandler(rec))

	publish(t, ch, notifmsg.EventDelivered, event(t, notifmsg.EventDelivered, map[string]any{
		"errandId": "e", "errandVersion": 1, "event": "deliver",
		"to": "DELIVERED", "actorType": "COURIER", "actorId": "cour",
		"requesterId": "req", "courierId": "cour", "reward": 10, "at": "2026-01-02T03:04:05Z",
	}))

	require.Eventually(t, func() bool { return len(rec.snapshot()) == 1 },
		10*time.Second, 20*time.Millisecond, "the notification is recorded")
	items := rec.snapshot()
	require.Equal(t, "req", items[0].Recipient, "the delivering courier is not notified of their own action")
	require.Eventually(t, func() bool { return queueLength(t, ch, notifmsg.QueueErrands) == 0 },
		5*time.Second, 20*time.Millisecond, "the event is acked")
	require.Zero(t, queueLength(t, ch, notifmsg.QueueErrandsDLQ))
}

func TestConsumerDeadLettersUndecodableEvents(t *testing.T) {
	ch := brokerChannel(t)
	rec := &recorder{}
	startConsumer(t, notifmsg.NewHandler(rec))

	// Bad JSON and a recognised-but-unknown errand key both dead-letter.
	publish(t, ch, notifmsg.EventDelivered, []byte("not json"))
	publish(t, ch, "errand.teleported", event(t, "errand.teleported", map[string]any{
		"errandId": "e", "errandVersion": 1, "requesterId": "req",
	}))

	require.Eventually(t, func() bool { return queueLength(t, ch, notifmsg.QueueErrandsDLQ) == 2 },
		10*time.Second, 20*time.Millisecond)
	require.Zero(t, queueLength(t, ch, notifmsg.QueueErrands))
	require.Empty(t, rec.snapshot())
}

func TestOnlyErrandKeysReachTheQueue(t *testing.T) {
	ch := brokerChannel(t)
	startConsumer(t, notifmsg.NewHandler(&recorder{}))

	returned := ch.NotifyReturn(make(chan amqp.Return, 1))
	// credit.* is published to the same exchange but must not bind to our
	// errand.# queue.
	publish(t, ch, "credit.reserved", []byte(`{}`))

	select {
	case r := <-returned:
		require.Equal(t, uint16(312), r.ReplyCode, "NO_ROUTE")
	case <-time.After(5 * time.Second):
		t.Fatal("a non-errand routing key should be returned as unroutable")
	}
}

func TestConsumerRetriesATemporaryFailureThenDeadLetters(t *testing.T) {
	ch := brokerChannel(t)
	var calls atomic.Int32
	startConsumer(t, handlerFunc(func(context.Context, string, []byte) error {
		calls.Add(1)
		return errors.New("database unavailable")
	}))

	publish(t, ch, notifmsg.EventOpened, []byte(`{}`))

	require.Eventually(t, func() bool { return queueLength(t, ch, notifmsg.QueueErrandsDLQ) == 1 },
		10*time.Second, 20*time.Millisecond)
	require.EqualValues(t, notifmsg.DeliveryLimit, calls.Load(), "tried up to the attempt limit")
}

func TestConsumerRecoversFromATemporaryFailure(t *testing.T) {
	ch := brokerChannel(t)
	var calls atomic.Int32
	startConsumer(t, handlerFunc(func(context.Context, string, []byte) error {
		if calls.Add(1) < 3 {
			return errors.New("database unavailable")
		}
		return nil
	}))

	publish(t, ch, notifmsg.EventOpened, []byte(`{}`))

	require.Eventually(t, func() bool { return calls.Load() == 3 && queueLength(t, ch, notifmsg.QueueErrands) == 0 },
		10*time.Second, 20*time.Millisecond)
	require.Zero(t, queueLength(t, ch, notifmsg.QueueErrandsDLQ))
}
