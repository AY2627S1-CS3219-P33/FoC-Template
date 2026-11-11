// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-10
// Scope: Implementation of test code (unit and integration tests).
// Author review: Reviewed, validated, and tested by the authors.

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

// The room-closing flows from the plan, through a real RabbitMQ. The
// consumer handles one delivery at a time, so events are applied in the
// order they are published here, which lets each case choose its order.
func TestConsumerClosesRooms(t *testing.T) {
	ch := brokerChannel(t)
	rooms := store.New(testdb.Migrated(t).Pool)
	startConsumer(t, chat.NewLifecycle(rooms, 24*time.Hour, nil))

	const (
		courierA = "auth0|courier-a"
		courierB = "auth0|courier-b"
	)
	t0 := time.Date(2026, 11, 9, 14, 0, 0, 0, time.UTC)
	at := func(minutes int) string { return t0.Add(time.Duration(minutes) * time.Minute).Format(time.RFC3339) }
	by := func(courier string) func(env, data map[string]any) {
		return func(_, data map[string]any) { data["courierId"] = courier }
	}

	// waitForEnding waits until the courier's room has the ending, then
	// checks it ended at minute and closes a day later.
	waitForEnding := func(t *testing.T, errandID uuid.UUID, courier string, reason chat.EndReason, minute int) {
		t.Helper()
		want := t0.Add(time.Duration(minute) * time.Minute)
		require.Eventually(t, func() bool {
			listed, err := rooms.ListRoomsForErrand(context.Background(), errandID)
			if err != nil {
				return false
			}
			for _, room := range listed {
				if room.CourierID == courier && room.Ending != nil && room.Ending.Reason == reason {
					return want.Equal(room.Ending.EndedAt) && want.Add(24*time.Hour).Equal(room.Ending.ClosesAt)
				}
			}
			return false
		}, 10*time.Second, 20*time.Millisecond, "room of %s should end with %s at minute %d", courier, reason, minute)
	}
	drained := func(t *testing.T) {
		t.Helper()
		require.Eventually(t, func() bool { return queueLength(t, ch, Queue) == 0 }, 10*time.Second, 20*time.Millisecond)
		require.Equal(t, 0, queueLength(t, ch, DeadLetterQueue))
	}

	t.Run("accepted then completed, each delivered twice", func(t *testing.T) {
		errand := uuid.New()
		id := errand.String()
		for range 2 {
			publish(t, ch, RoutingErrandAccepted, eventBody(t, RoutingErrandAccepted, id, 2, at(0), by(courierA)))
		}
		for range 2 {
			publish(t, ch, RoutingErrandCompleted, eventBody(t, RoutingErrandCompleted, id, 6, at(40), by(courierA)))
		}
		waitForEnding(t, errand, courierA, chat.EndCompleted, 40)
		drained(t)
		listed, err := rooms.ListRoomsForErrand(context.Background(), errand)
		require.NoError(t, err)
		require.Len(t, listed, 1)
	})

	t.Run("refunded", func(t *testing.T) {
		errand := uuid.New()
		id := errand.String()
		publish(t, ch, RoutingErrandAccepted, eventBody(t, RoutingErrandAccepted, id, 2, at(0), by(courierA)))
		publish(t, ch, RoutingErrandRefunded, eventBody(t, RoutingErrandRefunded, id, 8, at(90), by(courierA)))
		waitForEnding(t, errand, courierA, chat.EndRefunded, 90)
		drained(t)
	})

	t.Run("withdrawn before accepted, each delivered twice", func(t *testing.T) {
		errand := uuid.New()
		id := errand.String()
		for range 2 {
			publish(t, ch, RoutingErrandWithdrawn, eventBody(t, RoutingErrandWithdrawn, id, 3, at(10), by(courierA)))
		}
		for range 2 {
			publish(t, ch, RoutingErrandAccepted, eventBody(t, RoutingErrandAccepted, id, 2, at(0), by(courierA)))
		}
		waitForEnding(t, errand, courierA, chat.EndWithdrawn, 10)
		require.Eventually(t, func() bool {
			listed, err := rooms.ListRoomsForErrand(context.Background(), errand)
			return err == nil && len(listed) == 1 && listed[0].OpenedVersion != nil
		}, 10*time.Second, 20*time.Millisecond, "the late acceptance fills in the version")
		drained(t)
		listed, err := rooms.ListRoomsForErrand(context.Background(), errand)
		require.NoError(t, err)
		require.Equal(t, chat.EndWithdrawn, listed[0].Ending.Reason, "and does not reopen the room")
	})

	t.Run("completed before accepted", func(t *testing.T) {
		errand := uuid.New()
		id := errand.String()
		publish(t, ch, RoutingErrandCompleted, eventBody(t, RoutingErrandCompleted, id, 6, at(40), by(courierA)))
		publish(t, ch, RoutingErrandAccepted, eventBody(t, RoutingErrandAccepted, id, 2, at(0), by(courierA)))
		waitForEnding(t, errand, courierA, chat.EndCompleted, 40)
		drained(t)
	})

	t.Run("withdrawal, new courier, completion", func(t *testing.T) {
		errand := uuid.New()
		id := errand.String()
		publish(t, ch, RoutingErrandAccepted, eventBody(t, RoutingErrandAccepted, id, 2, at(0), by(courierA)))
		publish(t, ch, RoutingErrandWithdrawn, eventBody(t, RoutingErrandWithdrawn, id, 3, at(10), by(courierA)))
		publish(t, ch, RoutingErrandAccepted, eventBody(t, RoutingErrandAccepted, id, 4, at(20), by(courierB)))
		publish(t, ch, RoutingErrandCompleted, eventBody(t, RoutingErrandCompleted, id, 7, at(90), by(courierB)))
		waitForEnding(t, errand, courierB, chat.EndCompleted, 90)
		waitForEnding(t, errand, courierA, chat.EndWithdrawn, 10) // A keeps its earlier end
		drained(t)
	})
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
	publish(t, ch, RoutingErrandWithdrawn, eventBody(t, RoutingErrandWithdrawn, errandID.String(), 3, "2026-11-09T14:10:00Z",
		func(_, data map[string]any) { delete(data, "courierId") })) // whose room would end?

	require.Eventually(t, func() bool { return queueLength(t, ch, DeadLetterQueue) == 3 }, 10*time.Second, 20*time.Millisecond)
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
	if err := f.call(); err != nil {
		return chat.AcceptResult{}, err
	}
	return chat.AcceptResult{Opened: true}, nil
}

func (f *flakyApplier) OnWithdrawn(context.Context, chat.WithdrawnEvent) (chat.WithdrawResult, error) {
	return chat.WithdrawResult{}, f.call()
}

func (f *flakyApplier) OnErrandEnded(context.Context, chat.ErrandEndedEvent) (chat.EndResult, error) {
	return chat.EndResult{}, f.call()
}

func (f *flakyApplier) call() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls <= f.failures {
		return f.err
	}
	return nil
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
