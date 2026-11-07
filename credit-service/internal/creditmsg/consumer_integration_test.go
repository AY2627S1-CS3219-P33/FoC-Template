package creditmsg_test

import (
	"context"
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

	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/creditmsg"
)

// These tests need a RabbitMQ: set TEST_AMQP_URL, for example
// amqp://guest:guest@127.0.0.1:5673/ after
// `podman run -d -p 127.0.0.1:5673:5672 rabbitmq:4-alpine`. The tests delete
// and recreate the credit-service queues, so use a throwaway broker.

// testRetryDelay is how long a failed command waits on the retry queue in
// these tests. It is a queue argument, so brokerChannel deletes the queue
// before and after, or a later declaration with the real delay would be refused.
const testRetryDelay = 100 * time.Millisecond

// testMaxAttempts is the attempt limit that testConfig gives the consumer.
const testMaxAttempts = 5

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
	deleteQueues := func() {
		for _, queue := range []string{creditmsg.QueueCommands, creditmsg.QueueCommandsRetry, creditmsg.QueueCommandsDLQ} {
			_, err := ch.QueueDelete(queue, false, false, false)
			require.NoError(t, err)
		}
	}
	deleteQueues()
	restore := creditmsg.SetRetryDelay(testRetryDelay)
	// Cleanups run last in, first out: the consumer has stopped by now, and the
	// connection is closed after this.
	t.Cleanup(func() {
		deleteQueues()
		restore()
	})
	return ch
}

func testConfig() creditmsg.Config {
	return creditmsg.Config{
		MaxAttempts:      testMaxAttempts,
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
	require.EqualValues(t, testMaxAttempts, calls.Load(), "tried up to the attempt limit")
	require.Zero(t, queueLength(t, ch, creditmsg.QueueCommands))
	require.Zero(t, queueLength(t, ch, creditmsg.QueueCommandsRetry), "nothing is left waiting")
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
	require.Zero(t, queueLength(t, ch, creditmsg.QueueCommandsRetry))
}

// A parked command comes back as the same command: same routing key, body and
// message ID, so the handler and the stored result treat every try alike.
func TestARetriedCommandIsTheSameCommand(t *testing.T) {
	ch := brokerChannel(t)
	type try struct {
		routingKey string
		body       string
	}
	var mu sync.Mutex
	var tries []try
	startConsumer(t, handlerFunc(func(_ context.Context, routingKey string, body []byte) error {
		mu.Lock()
		defer mu.Unlock()
		tries = append(tries, try{routingKey, string(body)})
		if len(tries) < 3 {
			return errors.New("database unavailable")
		}
		return nil
	}))

	publishCommand(t, ch, "credit.transfer", []byte(`{"amount":7}`))

	require.Eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(tries) == 3 },
		10*time.Second, 20*time.Millisecond)
	for _, got := range tries {
		require.Equal(t, try{"credit.transfer", `{"amount":7}`}, got)
	}
}

// The point of the retry queue: a failing command waits there, so the commands
// behind it are not held up.
func TestAFailingCommandDoesNotHoldUpTheOnesBehindIt(t *testing.T) {
	ch := brokerChannel(t)
	// Restored by a cleanup that runs after the consumer has stopped, not by a
	// defer, which would change the delay under the running consumer.
	t.Cleanup(creditmsg.SetRetryDelay(2 * time.Second))
	var failing, healthy atomic.Int32
	startConsumer(t, handlerFunc(func(_ context.Context, _ string, body []byte) error {
		if string(body) == `{"id":"bad"}` {
			failing.Add(1)
			return errors.New("database unavailable")
		}
		healthy.Add(1)
		return nil
	}))

	publishCommand(t, ch, "credit.release", []byte(`{"id":"bad"}`))
	publishCommand(t, ch, "credit.release", []byte(`{"id":"good"}`))

	require.Eventually(t, func() bool { return healthy.Load() == 1 }, time.Second, 10*time.Millisecond,
		"the healthy command is handled while the bad one waits")
	require.EqualValues(t, 1, failing.Load(), "the bad one has not been tried again yet")
	require.Equal(t, 1, queueLength(t, ch, creditmsg.QueueCommandsRetry))
	require.Eventually(t, func() bool { return failing.Load() >= 2 }, 10*time.Second, 20*time.Millisecond,
		"and it is tried again after the delay")
}

// If the command cannot be parked, the consumer still keeps it: it requeues the
// original, and reconnects so that the topology is declared again.
func TestConsumerRequeuesWhenTheRetryQueueIsUnavailable(t *testing.T) {
	ch := brokerChannel(t)
	var calls atomic.Int32
	startConsumer(t, handlerFunc(func(context.Context, string, []byte) error {
		if calls.Add(1) == 1 {
			return errors.New("database unavailable")
		}
		return nil
	}))
	require.NoError(t, ch.ExchangeDelete(creditmsg.ExchangeCommandsRetry, false, false))

	publishCommand(t, ch, "credit.release", []byte(`{}`))

	require.Eventually(t, func() bool { return calls.Load() == 2 && queueLength(t, ch, creditmsg.QueueCommands) == 0 },
		10*time.Second, 20*time.Millisecond, "the command is tried again and acked")
	require.Zero(t, queueLength(t, ch, creditmsg.QueueCommandsDLQ))
	require.Eventually(t, func() bool {
		return ch.ExchangeDeclarePassive(creditmsg.ExchangeCommandsRetry, "direct", true, false, false, false, nil) == nil
	}, 10*time.Second, 50*time.Millisecond, "the reconnect declared the retry exchange again")
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
