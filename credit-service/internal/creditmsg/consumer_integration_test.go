package creditmsg_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/creditmsg"
)

// These tests need a RabbitMQ: set TEST_AMQP_URL, for example
// amqp://guest:guest@127.0.0.1:5673/ after
// `podman run -d -p 127.0.0.1:5673:5672 rabbitmq:4-alpine`. The tests delete
// and recreate the credit-service queues, so use a throwaway broker.

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
	for _, queue := range []string{creditmsg.QueueCommands, creditmsg.QueueCommandsDLQ} {
		_, err := ch.QueueDelete(queue, false, false, false)
		require.NoError(t, err)
	}
	return ch
}

func testConfig() creditmsg.Config {
	return creditmsg.Config{
		RetryBackoff:     creditmsg.Backoff{Base: time.Millisecond, Cap: 5 * time.Millisecond},
		ReconnectBackoff: creditmsg.Backoff{Base: 10 * time.Millisecond, Cap: 50 * time.Millisecond},
	}
}

// startConsumer runs a consumer until the test ends and waits until it is
// subscribed, which also means the topology exists.
func startConsumer(t *testing.T, handler creditmsg.MessageHandler) *creditmsg.Consumer {
	t.Helper()
	consumer, err := creditmsg.NewConsumer(os.Getenv("TEST_AMQP_URL"), handler,
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
	return consumer
}

func publishCommand(t *testing.T, ch *amqp.Channel, routingKey string, body []byte) {
	t.Helper()
	require.NoError(t, ch.PublishWithContext(context.Background(), creditmsg.ExchangeCommands, routingKey, true, false,
		amqp.Publishing{ContentType: "application/json", DeliveryMode: amqp.Persistent, Body: body}))
}

func queueLength(t *testing.T, ch *amqp.Channel, name string) int {
	t.Helper()
	q, err := ch.QueueInspect(name)
	require.NoError(t, err)
	return q.Messages
}

type handlerFunc func(ctx context.Context, routingKey string, body []byte) error

func (f handlerFunc) Handle(ctx context.Context, routingKey string, body []byte) error {
	return f(ctx, routingKey, body)
}

func TestConsumerAppliesACommandAndQueuesTheResult(t *testing.T) {
	ch := brokerChannel(t)
	f := newFixture(t)
	startConsumer(t, f.handler)

	publishCommand(t, ch, "credit.reserve", message(errand+":reserve", "credit.reserve", 1, reserveData(30)))

	require.Eventually(t, func() bool {
		_, err := f.outboxRow(errand + ":reserve:credit.reserved")
		return err == nil
	}, 10*time.Second, 20*time.Millisecond, "the result is queued")
	require.Eventually(t, func() bool { return queueLength(t, ch, creditmsg.QueueCommands) == 0 },
		5*time.Second, 20*time.Millisecond, "the command is acked")
	require.Zero(t, queueLength(t, ch, creditmsg.QueueCommandsDLQ))
}

func TestConsumerDeadLettersAMessageThatCanNeverSucceed(t *testing.T) {
	ch := brokerChannel(t)
	f := newFixture(t)
	startConsumer(t, f.handler)

	publishCommand(t, ch, "credit.reserve", []byte("not json"))

	require.Eventually(t, func() bool { return queueLength(t, ch, creditmsg.QueueCommandsDLQ) == 1 },
		10*time.Second, 20*time.Millisecond)
	require.Zero(t, queueLength(t, ch, creditmsg.QueueCommands))
	require.Zero(t, f.outboxCount(t))
}

func TestConsumerRetriesATemporaryFailureThenDeadLetters(t *testing.T) {
	ch := brokerChannel(t)
	var calls atomic.Int32
	startConsumer(t, handlerFunc(func(context.Context, string, []byte) error {
		calls.Add(1)
		return errors.New("database unavailable")
	}))

	publishCommand(t, ch, "credit.release", []byte(`{}`))

	require.Eventually(t, func() bool { return queueLength(t, ch, creditmsg.QueueCommandsDLQ) == 1 },
		10*time.Second, 20*time.Millisecond)
	require.EqualValues(t, creditmsg.DeliveryLimit, calls.Load(), "tried up to the attempt limit")
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

	publishCommand(t, ch, "credit.release", []byte(`{}`))

	require.Eventually(t, func() bool { return calls.Load() == 3 && queueLength(t, ch, creditmsg.QueueCommands) == 0 },
		10*time.Second, 20*time.Millisecond)
	require.Zero(t, queueLength(t, ch, creditmsg.QueueCommandsDLQ))
}

func TestConsumerSurvivesAPanic(t *testing.T) {
	ch := brokerChannel(t)
	var calls atomic.Int32
	startConsumer(t, handlerFunc(func(context.Context, string, []byte) error {
		if calls.Add(1) == 1 {
			panic("boom")
		}
		return nil
	}))

	publishCommand(t, ch, "credit.release", []byte(`{}`))

	require.Eventually(t, func() bool { return calls.Load() == 2 && queueLength(t, ch, creditmsg.QueueCommands) == 0 },
		10*time.Second, 20*time.Millisecond)
}

func TestOnlyCommandKeysReachTheQueue(t *testing.T) {
	ch := brokerChannel(t)
	startConsumer(t, handlerFunc(func(context.Context, string, []byte) error { return nil }))

	returned := ch.NotifyReturn(make(chan amqp.Return, 1))
	publishCommand(t, ch, "credit.refund", []byte(`{}`))

	select {
	case r := <-returned:
		require.Equal(t, uint16(312), r.ReplyCode, "NO_ROUTE")
	case <-time.After(5 * time.Second):
		t.Fatal("an unbound routing key should be returned as unroutable")
	}
}

func TestNewConsumerValidates(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := handlerFunc(func(context.Context, string, []byte) error { return nil })

	_, err := creditmsg.NewConsumer("", handler, logger, creditmsg.Config{})
	require.Error(t, err)
	_, err = creditmsg.NewConsumer("not a url", handler, logger, creditmsg.Config{})
	require.Error(t, err)
	_, err = creditmsg.NewConsumer("amqp://localhost:5672/", nil, logger, creditmsg.Config{})
	require.Error(t, err)
	_, err = creditmsg.NewConsumer("amqp://localhost:5672/", handler, logger, creditmsg.Config{Prefetch: 5000})
	require.Error(t, err)
	_, err = creditmsg.NewConsumer("amqp://localhost:5672/", handler, logger, creditmsg.Config{})
	require.NoError(t, err)
}
