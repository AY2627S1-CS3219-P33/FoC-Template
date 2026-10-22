package creditmsg_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/creditmsg"
	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/database/outboxrepo"
)

// These tests need the RabbitMQ of consumer_integration_test.go. They stand in
// for order-service by binding a queue of their own to credit.# on foc.events.

// resultsQueue binds a throwaway queue to credit.# and returns its name.
func resultsQueue(t *testing.T, ch *amqp.Channel) string {
	t.Helper()
	require.NoError(t, creditmsg.DeclareTopology(ch))
	q, err := ch.QueueDeclare("", false, true, true, false, nil)
	require.NoError(t, err)
	require.NoError(t, ch.QueueBind(q.Name, "credit.#", creditmsg.ExchangeEvents, false, nil))
	return q.Name
}

func newPublisher(t *testing.T) *creditmsg.Publisher {
	t.Helper()
	p, err := creditmsg.NewPublisher(os.Getenv("TEST_AMQP_URL"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func resultPayload(t *testing.T, typ, data string) []byte {
	t.Helper()
	body, err := json.Marshal(creditmsg.Envelope{
		MessageID: errand + ":reserve:" + typ, CorrelationID: errand + ":reserve", Type: typ,
		OccurredAt: time.Date(2026, 10, 14, 9, 0, 1, 0, time.UTC), ErrandID: errand, ErrandVersion: 1,
		Data: json.RawMessage(data),
	})
	require.NoError(t, err)
	return body
}

func nextMessage(t *testing.T, ch *amqp.Channel, queue string) amqp.Delivery {
	t.Helper()
	var d amqp.Delivery
	require.Eventually(t, func() bool {
		var ok bool
		d, ok, _ = ch.Get(queue, true)
		return ok
	}, 10*time.Second, 20*time.Millisecond, "a message on %s", queue)
	return d
}

func TestPublisherSendsAConfirmedPersistentResult(t *testing.T) {
	ch := brokerChannel(t)
	queue := resultsQueue(t, ch)
	payload := resultPayload(t, "credit.reserved", `{"amount": 5}`)

	require.NoError(t, newPublisher(t).Publish(context.Background(), "credit.reserved", payload))

	d := nextMessage(t, ch, queue)
	require.Equal(t, creditmsg.ExchangeEvents, d.Exchange)
	require.Equal(t, "credit.reserved", d.RoutingKey)
	require.Equal(t, errand+":reserve:credit.reserved", d.MessageId)
	require.Equal(t, errand+":reserve", d.CorrelationId)
	require.Equal(t, "credit.reserved", d.Type)
	require.Equal(t, "application/json", d.ContentType)
	require.Equal(t, amqp.Persistent, d.DeliveryMode)
	require.Equal(t, time.Date(2026, 10, 14, 9, 0, 1, 0, time.UTC), d.Timestamp.UTC())
	require.JSONEq(t, string(payload), string(d.Body))
}

func TestPublisherReusesItsSession(t *testing.T) {
	ch := brokerChannel(t)
	queue := resultsQueue(t, ch)
	p := newPublisher(t)

	for range 3 {
		require.NoError(t, p.Publish(context.Background(), "credit.released", resultPayload(t, "credit.released", `{}`)))
	}
	for range 3 {
		nextMessage(t, ch, queue)
	}
}

func TestPublisherReportsAnUnroutableResult(t *testing.T) {
	ch := brokerChannel(t)
	require.NoError(t, creditmsg.DeclareTopology(ch)) // the exchange exists, no queue is bound

	err := newPublisher(t).Publish(context.Background(), "credit.reserved", resultPayload(t, "credit.reserved", `{"amount": 5}`))

	require.ErrorIs(t, err, creditmsg.ErrUnroutable)
}

func TestPublisherRefusesAnInvalidMessageBeforeSendingIt(t *testing.T) {
	p := newPublisher(t)
	good := resultPayload(t, "credit.reserved", `{"amount": 5}`)
	for name, c := range map[string]struct {
		key     string
		payload []byte
	}{
		"not json":              {"credit.reserved", []byte("{")},
		"key is a command":      {"credit.reserve", resultPayload(t, "credit.reserve", `{}`)},
		"unknown key":           {"credit.refunded", resultPayload(t, "credit.refunded", `{}`)},
		"type differs from key": {"credit.released", good},
		"data not an object":    {"credit.reserved", resultPayload(t, "credit.reserved", `[1]`)},
		"data missing":          {"credit.reserved", resultPayload(t, "credit.reserved", `null`)},
	} {
		err := p.Publish(context.Background(), c.key, c.payload)
		require.ErrorIs(t, err, creditmsg.ErrUnpublishable, name)
	}

	var env creditmsg.Envelope
	require.NoError(t, json.Unmarshal(good, &env))
	for name, mutate := range map[string]func(*creditmsg.Envelope){
		"no correlation id": func(e *creditmsg.Envelope) { e.CorrelationID = "" },
		"no message id":     func(e *creditmsg.Envelope) { e.MessageID = "" },
		"no errand":         func(e *creditmsg.Envelope) { e.ErrandID = "" },
		"version zero":      func(e *creditmsg.Envelope) { e.ErrandVersion = 0 },
		"no time":           func(e *creditmsg.Envelope) { e.OccurredAt = time.Time{} },
	} {
		e := env
		mutate(&e)
		body, err := json.Marshal(e)
		require.NoError(t, err)
		require.ErrorIs(t, p.Publish(context.Background(), "credit.reserved", body), creditmsg.ErrUnpublishable, name)
	}
}

func TestPublisherReportsAnUnreachableBrokerWithoutTheURL(t *testing.T) {
	p, err := creditmsg.NewPublisher("amqp://user:secret-pass@127.0.0.1:1/")
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })
	payload := resultPayload(t, "credit.reserved", `{"amount": 5}`)

	err = p.Publish(context.Background(), "credit.reserved", payload)
	require.ErrorIs(t, err, creditmsg.ErrBrokerUnavailable)
	require.NotContains(t, err.Error(), "secret-pass")

	// The next call fails at once instead of dialling again.
	start := time.Now()
	err = p.Publish(context.Background(), "credit.reserved", payload)
	require.ErrorIs(t, err, creditmsg.ErrBrokerUnavailable)
	require.Less(t, time.Since(start), 100*time.Millisecond)
}

func TestPublisherRefusesWorkAfterClose(t *testing.T) {
	brokerChannel(t)
	p, err := creditmsg.NewPublisher(os.Getenv("TEST_AMQP_URL"))
	require.NoError(t, err)
	require.NoError(t, p.Close())

	require.Error(t, p.Publish(context.Background(), "credit.reserved", resultPayload(t, "credit.reserved", `{"amount": 5}`)))
}

func TestNewPublisherValidates(t *testing.T) {
	_, err := creditmsg.NewPublisher("")
	require.Error(t, err)
	_, err = creditmsg.NewPublisher("not a url")
	require.Error(t, err)
}

// The whole path inside credit-service: a command on foc.commands is consumed
// and applied, its result is queued in the outbox, and the dispatcher
// publishes it to foc.events.
func TestACommandProducesAResultOnTheBroker(t *testing.T) {
	ch := brokerChannel(t)
	queue := resultsQueue(t, ch)
	f := newFixture(t)
	startConsumer(t, f.handler)

	dispatcher, err := creditmsg.NewDispatcher(outboxrepo.Store{DB: f.pool}, newPublisher(t),
		slog.New(slog.NewTextHandler(io.Discard, nil)), creditmsg.DispatcherConfig{PollInterval: 10 * time.Millisecond})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- dispatcher.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		require.ErrorIs(t, <-done, context.Canceled)
	})

	publishCommand(t, ch, "credit.reserve", message(errand+":reserve", "credit.reserve", 1, reserveData(30)))

	d := nextMessage(t, ch, queue)
	require.Equal(t, "credit.reserved", d.RoutingKey)
	require.Equal(t, errand+":reserve:credit.reserved", d.MessageId)
	require.Equal(t, errand+":reserve", d.CorrelationId)
	var env creditmsg.Envelope
	require.NoError(t, json.Unmarshal(d.Body, &env))
	require.Equal(t, errand, env.ErrandID)
	require.JSONEq(t, `{"amount": 30}`, string(env.Data))

	require.Eventually(t, func() bool {
		var status string
		err := f.pool.QueryRow(context.Background(),
			`SELECT status FROM outbox WHERE idempotency_key = $1`, d.MessageId).Scan(&status)
		return err == nil && status == "SENT"
	}, 5*time.Second, 20*time.Millisecond, "the row is marked SENT")

	// Order-service did not get the result and sends the command again: the
	// stored result is queued again and published again.
	publishCommand(t, ch, "credit.reserve", message(errand+":reserve", "credit.reserve", 1, reserveData(30)))
	again := nextMessage(t, ch, queue)
	require.Equal(t, d.MessageId, again.MessageId, "the same messageId, so the receiver drops the duplicate")
	w, err := f.service.GetByOwner(context.Background(), requester)
	require.NoError(t, err)
	require.EqualValues(t, 70, w.Available, "credits were reserved once")
}

func TestDispatcherDeliversOnceTheResultQueueExists(t *testing.T) {
	ch := brokerChannel(t)
	require.NoError(t, creditmsg.DeclareTopology(ch)) // order-service has not declared its queue yet
	f := newFixture(t)
	require.NoError(t, f.handler.Handle(context.Background(), "credit.reserve",
		message(errand+":reserve", "credit.reserve", 1, reserveData(30))))
	dispatcher, err := creditmsg.NewDispatcher(outboxrepo.Store{DB: f.pool}, newPublisher(t),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		creditmsg.DispatcherConfig{RetryBackoff: creditmsg.Backoff{Base: time.Millisecond, Cap: 5 * time.Millisecond}})
	require.NoError(t, err)

	_, err = dispatcher.RunOnce(context.Background())
	require.NoError(t, err)
	var status, failure string
	require.NoError(t, f.pool.QueryRow(context.Background(),
		`SELECT status, last_failure_code FROM outbox`).Scan(&status, &failure))
	require.Equal(t, "PENDING", status, "kept for another try")
	require.Equal(t, "UNROUTABLE", failure)

	queue := resultsQueue(t, ch) // order-service starts
	time.Sleep(20 * time.Millisecond)
	_, err = dispatcher.RunOnce(context.Background())
	require.NoError(t, err)
	nextMessage(t, ch, queue)
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT status FROM outbox`).Scan(&status))
	require.Equal(t, "SENT", status)
}
