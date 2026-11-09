package events

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/facts"
)

// memoryRecorder dedupes by messageId like facts.Store does, so "committed"
// survives a redelivery.
type memoryRecorder struct {
	mu     sync.Mutex
	stored map[string]facts.Event
	calls  int
}

func (m *memoryRecorder) Apply(_ context.Context, e facts.Event) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.stored == nil {
		m.stored = map[string]facts.Event{}
	}
	if _, ok := m.stored[e.MessageID]; ok {
		return false, nil
	}
	m.stored[e.MessageID] = e
	return true, nil
}

func (m *memoryRecorder) ids() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.stored))
	for id := range m.stored {
		ids = append(ids, id)
	}
	return ids
}

func (m *memoryRecorder) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// broker connects to TEST_AMQP_URL, declares the real topology and empties
// both queues; the queues are deleted afterwards.
func broker(t *testing.T) (string, *amqp.Channel) {
	t.Helper()
	url := os.Getenv("TEST_AMQP_URL")
	if url == "" {
		t.Skip("TEST_AMQP_URL is not set; skipping RabbitMQ consumer integration test")
	}
	conn, err := amqp.Dial(url)
	require.NoError(t, err)
	ch, err := conn.Channel()
	require.NoError(t, err)
	require.NoError(t, DeclareTopology(ch))
	for _, queue := range []string{Queue, QueueDLQ} {
		_, err := ch.QueuePurge(queue, false)
		require.NoError(t, err)
	}
	t.Cleanup(func() {
		_, _ = ch.QueueDelete(Queue, false, false, false)
		_, _ = ch.QueueDelete(QueueDLQ, false, false, false)
		_ = conn.Close()
	})
	return url, ch
}

func publish(t *testing.T, ch *amqp.Channel, routingKey string, body []byte) {
	t.Helper()
	require.NoError(t, ch.PublishWithContext(context.Background(), ExchangeEvents, routingKey, false, false, amqp.Publishing{
		ContentType: "application/json", DeliveryMode: amqp.Persistent, Body: body,
	}))
}

func queueDepth(t *testing.T, ch *amqp.Channel, name string) int {
	t.Helper()
	q, err := ch.QueueInspect(name)
	require.NoError(t, err)
	return q.Messages
}

func startConsumer(t *testing.T, url string, recorder Recorder) (*Consumer, func()) {
	t.Helper()
	handler := NewHandler(recorder, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	consumer, err := NewConsumer(url, handler, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- consumer.Run(ctx) }()
	require.Eventually(t, consumer.Running, 5*time.Second, 10*time.Millisecond)
	return consumer, func() {
		cancel()
		select {
		case err := <-done:
			require.ErrorIs(t, err, context.Canceled)
		case <-time.After(5 * time.Second):
			t.Fatal("the consumer did not stop")
		}
		require.False(t, consumer.Running())
	}
}

func TestTopologyBindsOnlyErrandEvents(t *testing.T) {
	_, ch := broker(t)

	// A key outside errand.# is not routed to the queue; an errand.* key is.
	publish(t, ch, "credit.reserved", []byte("{}"))
	publish(t, ch, "errand.accepted", body("errand.accepted", 3, "OPEN", "ASSIGNED", ""))

	require.Eventually(t, func() bool { return queueDepth(t, ch, Queue) == 1 }, 3*time.Second, 20*time.Millisecond)
	time.Sleep(200 * time.Millisecond)
	require.Equal(t, 1, queueDepth(t, ch, Queue), "credit.# is not bound")
}

func TestConsumerRecordsEventsAndAcksThem(t *testing.T) {
	url, ch := broker(t)
	recorder := &memoryRecorder{}
	_, stop := startConsumer(t, url, recorder)
	defer stop()

	publish(t, ch, "errand.opened", body("errand.opened", 2, "PENDING_RESERVATION", "OPEN", ""))
	publish(t, ch, "errand.accepted", body("errand.accepted", 3, "OPEN", "ASSIGNED", ""))
	publish(t, ch, "errand.accept_rejected", []byte(`{"anything": true}`)) // not kept, still acked
	publish(t, ch, "errand.verification_reminder", []byte("not json"))     // not kept, still acked

	require.Eventually(t, func() bool { return len(recorder.ids()) == 2 }, 5*time.Second, 20*time.Millisecond)
	require.Eventually(t, func() bool {
		q, err := ch.QueueInspect(Queue)
		return err == nil && q.Messages == 0
	}, 5*time.Second, 20*time.Millisecond, "every message was acked")
	require.Zero(t, queueDepth(t, ch, QueueDLQ))
}

func TestConsumerDeadLettersWhatCanNeverBeProcessed(t *testing.T) {
	url, ch := broker(t)
	recorder := &memoryRecorder{}
	_, stop := startConsumer(t, url, recorder)
	defer stop()

	publish(t, ch, "errand.accepted", []byte("<html>not an event</html>"))
	publish(t, ch, "errand.completed", body("errand.completed", 8, "DELIVERED", "ASSIGNED", "")) // does not end terminal
	publish(t, ch, "errand.accepted", body("errand.accepted", 3, "OPEN", "ASSIGNED", ""))        // a good one after the bad

	require.Eventually(t, func() bool { return queueDepth(t, ch, QueueDLQ) == 2 }, 5*time.Second, 20*time.Millisecond,
		"undecodable messages dead-letter instead of blocking the queue")
	require.Eventually(t, func() bool { return len(recorder.ids()) == 1 }, 5*time.Second, 20*time.Millisecond)
	require.Eventually(t, func() bool { return queueDepth(t, ch, Queue) == 0 }, 5*time.Second, 20*time.Millisecond)
}

// A crash between the commit and the ack leaves the message unacknowledged;
// the broker delivers it again, and the second delivery changes nothing.
func TestRedeliveryAfterACrashBetweenCommitAndAck(t *testing.T) {
	url, ch := broker(t)
	recorder := &memoryRecorder{}
	msg := body("errand.accepted", 3, "OPEN", "ASSIGNED", "")
	publish(t, ch, "errand.accepted", msg)
	require.Eventually(t, func() bool { return queueDepth(t, ch, Queue) == 1 }, 3*time.Second, 20*time.Millisecond)

	// The "crashed" consumer: takes the delivery, commits the effect, and dies
	// before acking.
	crashing, err := amqp.Dial(url)
	require.NoError(t, err)
	crashCh, err := crashing.Channel()
	require.NoError(t, err)
	deliveries, err := crashCh.Consume(Queue, "", false, false, false, false, nil)
	require.NoError(t, err)
	var d amqp.Delivery
	select {
	case d = <-deliveries:
	case <-time.After(3 * time.Second):
		t.Fatal("no delivery")
	}
	event, err := Decode(d.RoutingKey, d.Body)
	require.NoError(t, err)
	applied, err := recorder.Apply(context.Background(), event)
	require.NoError(t, err)
	require.True(t, applied)
	require.NoError(t, crashing.Close()) // no ack

	_, stop := startConsumer(t, url, recorder)
	defer stop()

	require.Eventually(t, func() bool { return recorder.callCount() == 2 }, 5*time.Second, 20*time.Millisecond, "the message came again")
	require.Eventually(t, func() bool { return queueDepth(t, ch, Queue) == 0 }, 5*time.Second, 20*time.Millisecond, "and was acked")
	require.Equal(t, []string{errandID + ":accepted:3"}, recorder.ids(), "it is still one fact")
	require.Zero(t, queueDepth(t, ch, QueueDLQ))
}

func TestConsumerReconnectsAfterTheConnectionIsLost(t *testing.T) {
	url, ch := broker(t)
	recorder := &memoryRecorder{}
	consumer, stop := startConsumer(t, url, recorder)
	defer stop()

	// Deleting the queue makes the broker cancel the consumer, which ends the
	// session; redeclaring it lets the consumer's next session subscribe.
	_, err := ch.QueueDelete(Queue, false, false, false)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return !consumer.Running() }, 5*time.Second, 20*time.Millisecond)
	require.NoError(t, DeclareTopology(ch))

	require.Eventually(t, consumer.Running, 10*time.Second, 50*time.Millisecond, "it resubscribed")
	publish(t, ch, "errand.accepted", body("errand.accepted", 3, "OPEN", "ASSIGNED", ""))
	require.Eventually(t, func() bool { return len(recorder.ids()) == 1 }, 5*time.Second, 20*time.Millisecond)
}

func TestNewConsumerValidates(t *testing.T) {
	handler := NewHandler(&memoryRecorder{}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, err := NewConsumer("", handler, logger, Config{})
	require.Error(t, err)
	_, err = NewConsumer("not a url", handler, logger, Config{})
	require.Error(t, err)
	_, err = NewConsumer("amqp://x", nil, logger, Config{})
	require.Error(t, err)
	_, err = NewConsumer("amqp://x", handler, logger, Config{Prefetch: 5000})
	require.Error(t, err)
}
