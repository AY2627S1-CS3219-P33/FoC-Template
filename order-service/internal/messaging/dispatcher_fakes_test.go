package messaging_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/messaging"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/resilience"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/testfakes"
	"github.com/stretchr/testify/require"
)

// These tests run the dispatcher against the real outbox in Postgres, with
// testfakes standing in for RabbitMQ and credit-service.

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

func newDispatcher(t *testing.T, outbox messaging.Outbox, pub errand.Publisher, settings errand.Settings, cfg messaging.DispatcherConfig) *messaging.Dispatcher {
	t.Helper()
	if settings == nil {
		settings = testfakes.NewFakeSettings()
	}
	d, err := messaging.NewDispatcher(outbox, pub, settings, discard, cfg)
	require.NoError(t, err)
	return d
}

func TestReserveRowIsPublishedOnceThenSent(t *testing.T) {
	pool, repo := messaging.TestDatabase(t)
	id, key := messaging.InsertMessage(t, pool, errand.KindCreditReserve)
	eventID, eventKey := messaging.InsertMessage(t, pool, errand.ErrandPlaced.Kind())
	pub := testfakes.NewFakePublisher()
	d := newDispatcher(t, repo, pub, nil, messaging.DispatcherConfig{PollInterval: 10 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	require.Eventually(t, func() bool {
		return messaging.RowStatus(t, pool, id) == "SENT" && messaging.RowStatus(t, pool, eventID) == "SENT"
	}, 5*time.Second, 10*time.Millisecond)
	time.Sleep(50 * time.Millisecond) // a few more polls
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)

	commands := pub.MessagesTo(errand.ExchangeCommands)
	require.Len(t, commands, 1, "published once")
	require.Equal(t, errand.CommandCreditReserve, commands[0].RoutingKey)
	require.Equal(t, key, commands[0].Envelope.MessageID)
	events := pub.MessagesTo(errand.ExchangeEvents)
	require.Len(t, events, 1)
	require.Equal(t, string(errand.ErrandPlaced), events[0].RoutingKey)
	require.Equal(t, eventKey, events[0].Envelope.MessageID)
	require.Len(t, pub.Attempts(), 2)
}

func TestPublishFailuresBackOffThenFail(t *testing.T) {
	pool, repo := messaging.TestDatabase(t)
	ctx := context.Background()
	id, _ := messaging.InsertMessage(t, pool, errand.KindCreditRelease)
	pub := testfakes.NewFakePublisher()
	pub.FailAlways("Publish", errors.New("broker unreachable"))
	counters := messaging.NewCounters()
	d := newDispatcher(t, repo, pub, nil, messaging.DispatcherConfig{
		MaxAttempts: 3, Metrics: counters,
		// Always the ceiling, so the delays are exact: 1s, 2s.
		Backoff: resilience.BackoffPolicy{Base: time.Second, Cap: time.Hour, Rand: func(n int64) int64 { return n - 1 }},
	})
	var delays []float64
	for attempt := 1; attempt <= 3; attempt++ {
		_, err := d.DispatchOnce(ctx, true)
		require.NoError(t, err)
		if attempt == 3 {
			break
		}
		var delay float64
		require.NoError(t, pool.QueryRow(ctx, "SELECT extract(epoch FROM next_attempt_at - clock_timestamp())::float8 FROM outbox WHERE id=$1", id).Scan(&delay))
		delays = append(delays, delay)
		require.Equal(t, "PENDING", messaging.RowStatus(t, pool, id))
		_, err = pool.Exec(ctx, "UPDATE outbox SET next_attempt_at=clock_timestamp() WHERE id=$1", id)
		require.NoError(t, err)
	}
	require.InDelta(t, 1, delays[0], 0.5)
	require.InDelta(t, 2, delays[1], 0.5)
	require.Greater(t, delays[1], delays[0], "next_attempt_at grows")

	var code string
	require.NoError(t, pool.QueryRow(ctx, "SELECT last_failure_code FROM outbox WHERE id=$1", id).Scan(&code))
	require.Equal(t, "FAILED", messaging.RowStatus(t, pool, id))
	require.Equal(t, messaging.CodePublishFailed, code)
	require.Len(t, pub.Attempts(), 3)
	require.Empty(t, pub.Messages())
	require.EqualValues(t, 2, counters.Count(messaging.CounterRescheduled, errand.KindCreditRelease))
	require.EqualValues(t, 1, counters.Count(messaging.CounterFailed, errand.KindCreditRelease))
}

// F4.6.2: a release whose result is lost is re-sent after the confirm
// timeout with the same messageId, and credit-service applies it once.
func TestUnconfirmedReleaseIsResentAndAppliedOnce(t *testing.T) {
	pool, repo := messaging.TestDatabase(t)
	ctx := context.Background()
	credit := testfakes.NewFakeCreditService(testfakes.NewFakeClock(time.Now()))
	credit.SetBalance("auth0|req1", 10)
	pub := testfakes.NewFakePublisher()
	settings := testfakes.NewFakeSettings()
	s := errand.DefaultSystemSettings()
	s.OutboxConfirmTimeout = time.Minute
	settings.Set(s)
	counters := messaging.NewCounters()
	d := newDispatcher(t, repo, pub, settings, messaging.DispatcherConfig{Metrics: counters})

	reserveID, reserveKey := messaging.InsertMessage(t, pool, errand.KindCreditReserve)
	_, err := d.DispatchOnce(ctx, true)
	require.NoError(t, err)
	results, err := credit.ConsumeFrom(pub)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, errand.ResultCreditReserved, results[0].RoutingKey)
	_, err = repo.ConfirmByKey(ctx, results[0].Envelope.CorrelationID)
	require.NoError(t, err)
	require.Equal(t, "CONFIRMED", messaging.RowStatus(t, pool, reserveID))
	require.Equal(t, 5, credit.Balance("auth0|req1"))

	releaseID, releaseKey := messaging.InsertCommand(t, pool, reserveID, errand.KindCreditRelease, errand.CreditCommand{ErrandVersion: 2, RequesterID: "auth0|req1"})
	credit.Script(errand.CommandCreditRelease, testfakes.CreditScript{Outcome: testfakes.CreditNoReply})
	_, err = d.DispatchOnce(ctx, true)
	require.NoError(t, err)
	results, err = credit.ConsumeFrom(pub)
	require.NoError(t, err)
	require.Empty(t, results, "the reply is lost")
	require.Equal(t, "SENT", messaging.RowStatus(t, pool, releaseID))
	require.Equal(t, 10, credit.Balance("auth0|req1"))

	// Not overdue yet.
	require.NoError(t, d.SweepOnce(ctx))
	require.Equal(t, "SENT", messaging.RowStatus(t, pool, releaseID))

	_, err = pool.Exec(ctx, "UPDATE outbox SET sent_at=clock_timestamp()-interval '2 minutes'")
	require.NoError(t, err)
	require.NoError(t, d.SweepOnce(ctx))
	require.Equal(t, "PENDING", messaging.RowStatus(t, pool, releaseID))
	require.Equal(t, "CONFIRMED", messaging.RowStatus(t, pool, reserveID), "a CONFIRMED row is never re-sent")
	require.EqualValues(t, 1, counters.Count(messaging.CounterResent, errand.KindCreditRelease))
	require.Zero(t, counters.Count(messaging.CounterResent, errand.KindCreditReserve))

	_, err = d.DispatchOnce(ctx, true)
	require.NoError(t, err)
	results, err = credit.ConsumeFrom(pub)
	require.NoError(t, err)
	require.Len(t, results, 1, "the redelivery returns the lost result")
	require.Equal(t, errand.ResultCreditReleased, results[0].RoutingKey)
	require.Equal(t, releaseKey, results[0].Envelope.CorrelationID)
	require.Equal(t, 10, credit.Balance("auth0|req1"), "released once")
	_, err = repo.ConfirmByKey(ctx, releaseKey)
	require.NoError(t, err)
	require.Equal(t, "CONFIRMED", messaging.RowStatus(t, pool, releaseID))

	var releases []string
	for _, m := range pub.MessagesTo(errand.ExchangeCommands) {
		if m.RoutingKey == errand.CommandCreditRelease {
			releases = append(releases, m.Envelope.MessageID)
		}
	}
	require.Equal(t, []string{releaseKey, releaseKey}, releases, "re-sent with the same messageId")
	require.NotEqual(t, reserveKey, releaseKey)

	// Confirmed rows stay put however old they are.
	_, err = pool.Exec(ctx, "UPDATE outbox SET sent_at=clock_timestamp()-interval '1 day'")
	require.NoError(t, err)
	require.NoError(t, d.SweepOnce(ctx))
	require.Equal(t, "CONFIRMED", messaging.RowStatus(t, pool, releaseID))
}

func TestParallelDispatchersNeverPublishTheSameRow(t *testing.T) {
	pool, repo := messaging.TestDatabase(t)
	var ids []int64
	for range 15 {
		id, _ := messaging.InsertMessage(t, pool, errand.KindCreditReserve)
		ids = append(ids, id)
		id, _ = messaging.InsertMessage(t, pool, errand.ErrandPlaced.Kind())
		ids = append(ids, id)
	}
	pub := testfakes.NewFakePublisher()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 2)
	for _, worker := range []string{"a", "b"} {
		d := newDispatcher(t, repo, pub, nil, messaging.DispatcherConfig{WorkerID: worker, BatchSize: 3, PollInterval: 5 * time.Millisecond})
		go func() { done <- d.Run(ctx) }()
	}
	require.Eventually(t, func() bool {
		for _, id := range ids {
			if messaging.RowStatus(t, pool, id) != "SENT" {
				return false
			}
		}
		return true
	}, 10*time.Second, 20*time.Millisecond)
	cancel()
	for range 2 {
		require.ErrorIs(t, <-done, context.Canceled)
	}
	seen := map[string]int{}
	for _, m := range pub.Attempts() {
		seen[m.Envelope.MessageID]++
	}
	require.Len(t, seen, len(ids))
	for key, n := range seen {
		require.Equal(t, 1, n, "row %s published %d times", key, n)
	}
}
