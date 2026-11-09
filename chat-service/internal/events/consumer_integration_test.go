package events

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/chat"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/store"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/testdb"
)

// These tests need a throwaway RabbitMQ (TEST_AMQP_URL, see
// compose.test.yaml): they delete and recreate chat-service's queues on it.
// Run them with go test -p 1.

func brokerChannel(t *testing.T) *amqp.Channel {
	t.Helper()
	url := os.Getenv("TEST_AMQP_URL")
	if url == "" {
		t.Skip("TEST_AMQP_URL is not set; skipping RabbitMQ test")
	}
	conn, err := amqp.Dial(url)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	ch, err := conn.Channel()
	require.NoError(t, err)
	for _, queue := range []string{Queue, DeadLetterQueue} {
		_, err := ch.QueueDelete(queue, false, false, false)
		require.NoError(t, err)
	}
	return ch
}

// startConsumer runs a consumer until the test ends and waits until it is
// subscribed, which also means the topology exists.
func startConsumer(t *testing.T, applier Applier) *Consumer {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	consumer, err := NewConsumer(os.Getenv("TEST_AMQP_URL"), NewHandler(applier, logger), logger, Config{
		RetryBackoff:     Backoff{Base: time.Millisecond, Cap: 5 * time.Millisecond},
		ReconnectBackoff: Backoff{Base: 10 * time.Millisecond, Cap: 50 * time.Millisecond},
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- consumer.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		require.ErrorIs(t, <-done, context.Canceled)
	})
	require.Eventually(t, consumer.Running, 10*time.Second, 10*time.Millisecond)
	return consumer
}

// publish sends an event the way order-service's publisher does.
func publish(t *testing.T, ch *amqp.Channel, routingKey string, body []byte) {
	t.Helper()
	require.NoError(t, ch.PublishWithContext(context.Background(), Exchange, routingKey, true, false, amqp.Publishing{
		ContentType: "application/json", DeliveryMode: amqp.Persistent,
		MessageId: uuid.NewString(), Type: routingKey, Body: body,
	}))
}

func queueLength(t *testing.T, ch *amqp.Channel, name string) int {
	t.Helper()
	q, err := ch.QueueInspect(name)
	require.NoError(t, err)
	return q.Messages
}

func TestConsumerCreatesARoomFromErrandAccepted(t *testing.T) {
	ctx := context.Background()
	ch := brokerChannel(t)
	rooms := store.New(testdb.Migrated(t).Pool)
	startConsumer(t, chat.NewLifecycle(rooms, 24*time.Hour, nil))

	errandID := uuid.New()
	body := acceptedBody(t, errandID.String(), nil)
	publish(t, ch, RoutingErrandAccepted, body)
	publish(t, ch, RoutingErrandAccepted, body) // at-least-once: a duplicate

	require.Eventually(t, func() bool {
		listed, err := rooms.ListRoomsForErrand(ctx, errandID)
		return err == nil && len(listed) == 1
	}, 10*time.Second, 20*time.Millisecond)
	require.Eventually(t, func() bool { return queueLength(t, ch, Queue) == 0 }, 10*time.Second, 20*time.Millisecond)

	listed, err := rooms.ListRoomsForErrand(ctx, errandID)
	require.NoError(t, err)
	require.Len(t, listed, 1, "the duplicate created nothing")
	require.Equal(t, "auth0|requester", listed[0].RequesterID)
	require.Equal(t, "auth0|courier", listed[0].CourierID)
	require.Equal(t, 0, queueLength(t, ch, DeadLetterQueue))
}

func TestBrokenEventsGoToTheDeadLetterQueue(t *testing.T) {
	ctx := context.Background()
	ch := brokerChannel(t)
	rooms := store.New(testdb.Migrated(t).Pool)
	startConsumer(t, chat.NewLifecycle(rooms, 24*time.Hour, nil))

	errandID := uuid.New()
	publish(t, ch, RoutingErrandAccepted, []byte("{not json"))
	publish(t, ch, RoutingErrandAccepted, acceptedBody(t, errandID.String(), func(_, data map[string]any) {
		data["courierId"] = data["requesterId"] // a requester cannot deliver their own errand
	}))

	require.Eventually(t, func() bool { return queueLength(t, ch, DeadLetterQueue) == 2 }, 10*time.Second, 20*time.Millisecond)
	require.Equal(t, 0, queueLength(t, ch, Queue))
	listed, err := rooms.ListRoomsForErrand(ctx, errandID)
	require.NoError(t, err)
	require.Empty(t, listed)
}

// flakyApplier fails with err a number of times, then works.
type flakyApplier struct {
	mu       sync.Mutex
	err      error
	failures int
	calls    int
}

func (f *flakyApplier) OnAccepted(context.Context, chat.AcceptedEvent) (chat.AcceptResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls <= f.failures {
		return chat.AcceptResult{}, f.err
	}
	return chat.AcceptResult{Opened: true}, nil
}

var errDatabaseDown = errors.Join(chat.ErrUnavailable, errors.New("database down"))

func (f *flakyApplier) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// A database outage must not lose the room: the event is retried for as long
// as it takes, well past MaxAttempts and RabbitMQ's DeliveryLimit.
func TestADatabaseOutageIsRetriedUntilItEnds(t *testing.T) {
	ch := brokerChannel(t)
	applier := &flakyApplier{err: errDatabaseDown, failures: 3 * MaxAttempts}
	startConsumer(t, applier)

	publish(t, ch, RoutingErrandAccepted, acceptedBody(t, uuid.NewString(), nil))
	require.Eventually(t, func() bool { return applier.callCount() == 3*MaxAttempts+1 }, 20*time.Second, 10*time.Millisecond,
		"applied once the database is back")
	require.Eventually(t, func() bool { return queueLength(t, ch, Queue) == 0 }, 10*time.Second, 20*time.Millisecond)
	require.Equal(t, 0, queueLength(t, ch, DeadLetterQueue))
}

func TestAnEventThatKeepsFailingUnexpectedlyIsDeadLettered(t *testing.T) {
	ch := brokerChannel(t)
	applier := &flakyApplier{err: errors.New("a bug"), failures: 1_000}
	startConsumer(t, applier)

	publish(t, ch, RoutingErrandAccepted, acceptedBody(t, uuid.NewString(), nil))
	require.Eventually(t, func() bool { return queueLength(t, ch, DeadLetterQueue) == 1 }, 20*time.Second, 20*time.Millisecond)
	require.Equal(t, MaxAttempts, applier.callCount(), "tried MaxAttempts times, then given up")
	require.Equal(t, 0, queueLength(t, ch, Queue))
}

// RabbitMQ's own limit still protects against an event that crashes the
// consumer: deliveries that are never answered count towards DeliveryLimit.
func TestDeliveriesNeverAnsweredAreDeadLetteredByRabbitMQ(t *testing.T) {
	ch := brokerChannel(t)
	require.NoError(t, Declare(ch))
	publish(t, ch, RoutingErrandAccepted, []byte("{}"))
	for attempt := 1; attempt <= DeliveryLimit+1; attempt++ {
		conn, err := amqp.Dial(os.Getenv("TEST_AMQP_URL"))
		require.NoError(t, err)
		consumerCh, err := conn.Channel()
		require.NoError(t, err)
		deliveries, err := consumerCh.Consume(Queue, "", false, false, false, false, nil)
		require.NoError(t, err)
		select {
		case <-deliveries:
		case <-time.After(5 * time.Second):
			t.Fatalf("delivery %d did not arrive", attempt)
		}
		require.NoError(t, conn.Close()) // a crash: the delivery is never answered
	}
	require.Eventually(t, func() bool { return queueLength(t, ch, DeadLetterQueue) == 1 }, 10*time.Second, 20*time.Millisecond)
	require.Equal(t, 0, queueLength(t, ch, Queue))
}

func TestDeclareIsIdempotentAndMatchesOrderService(t *testing.T) {
	ch := brokerChannel(t)
	require.NoError(t, Declare(ch))
	require.NoError(t, Declare(ch), "declaring again on every connect is harmless")
	// order-service declares the exchange with exactly these arguments; a
	// mismatch would close the channel with PRECONDITION_FAILED.
	require.NoError(t, ch.ExchangeDeclare(Exchange, "topic", true, false, false, false, nil))
}
