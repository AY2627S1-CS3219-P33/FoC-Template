// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-10-20
// Scope: Implementation of test code (unit and integration tests).
// Author review: Reviewed, validated, and tested by the authors.

package messaging_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/msgrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/messaging"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/resilience"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/testfakes"
	"github.com/stretchr/testify/require"
)

// These tests run the dispatcher against the real outbox in Postgres, with
// testfakes standing in for RabbitMQ and credit-service.

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

func newDispatcher(t *testing.T, outbox messaging.Outbox, pub errand.Publisher, cfg messaging.DispatcherConfig) *messaging.Dispatcher {
	t.Helper()
	d, err := messaging.NewDispatcher(outbox, pub, discard, cfg)
	require.NoError(t, err)
	return d
}

func TestReserveRowIsPublishedOnceThenSent(t *testing.T) {
	pool, repo := messaging.TestDatabase(t)
	id, key := messaging.InsertMessage(t, pool, errand.KindCreditReserve)
	eventID, eventKey := messaging.InsertMessage(t, pool, errand.ErrandPlaced.Kind())
	pub := testfakes.NewFakePublisher()
	d := newDispatcher(t, repo, pub, messaging.DispatcherConfig{PollInterval: 10 * time.Millisecond})
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

func TestPublishFailuresBackOffAndKeepRetrying(t *testing.T) {
	pool, repo := messaging.TestDatabase(t)
	ctx := context.Background()
	id, _ := messaging.InsertMessage(t, pool, errand.KindCreditRelease)
	pub := testfakes.NewFakePublisher()
	pub.FailAlways("Publish", errors.New("broker unreachable"))
	counters := messaging.NewCounters()
	d := newDispatcher(t, repo, pub, messaging.DispatcherConfig{
		Metrics: counters,
		// Always the ceiling, so the delays are exact: 1s, 2s, 4s, then the 4s cap.
		Backoff: resilience.BackoffPolicy{Base: time.Second, Cap: 4 * time.Second, Rand: func(n int64) int64 { return n - 1 }},
	})
	const attempts = 15 // more than the old limit of 10
	var delays []float64
	for range attempts {
		_, err := d.DispatchOnce(ctx, true)
		require.NoError(t, err)
		var delay float64
		require.NoError(t, pool.QueryRow(ctx, "SELECT extract(epoch FROM next_attempt_at - clock_timestamp())::float8 FROM outbox WHERE id=$1", id).Scan(&delay))
		delays = append(delays, delay)
		require.Equal(t, "PENDING", messaging.RowStatus(t, pool, id), "a publish failure never fails the row")
		_, err = pool.Exec(ctx, "UPDATE outbox SET next_attempt_at=clock_timestamp() WHERE id=$1", id)
		require.NoError(t, err)
	}
	require.InDelta(t, 1, delays[0], 0.5)
	require.InDelta(t, 2, delays[1], 0.5)
	require.InDelta(t, 4, delays[2], 0.5)
	require.InDelta(t, 4, delays[attempts-1], 0.5, "backoff stays at the cap")

	var failureCode string
	require.NoError(t, pool.QueryRow(ctx, "SELECT last_failure_code FROM outbox WHERE id=$1", id).Scan(&failureCode))
	require.Equal(t, messaging.CodePublishFailed, failureCode)
	require.Len(t, pub.Attempts(), attempts)
	require.Empty(t, pub.Messages())
	require.EqualValues(t, attempts, counters.Count(messaging.CounterRescheduled, errand.KindCreditRelease))
	require.Zero(t, counters.Count(messaging.CounterFailed, errand.KindCreditRelease))

	// The broker comes back: the same row goes out and is SENT.
	pub.FailAlways("Publish", nil)
	_, err := d.DispatchOnce(ctx, true)
	require.NoError(t, err)
	require.Equal(t, "SENT", messaging.RowStatus(t, pool, id))
	require.Len(t, pub.Messages(), 1)
}

// A release whose result is lost stays SENT. Nothing resends it by itself:
// the dispatcher leaves it alone, it shows up as overdue, and an admin requeue
// sends it again with the same messageId, which credit-service applies once.
func TestUnconfirmedReleaseIsListedAndRequeuedOnce(t *testing.T) {
	pool, repo := messaging.TestDatabase(t)
	ctx := context.Background()
	credit := testfakes.NewFakeCreditService(testfakes.NewFakeClock(time.Now()))
	credit.SetBalance("auth0|req1", 10)
	pub := testfakes.NewFakePublisher()
	d := newDispatcher(t, repo, pub, messaging.DispatcherConfig{})

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

	overdue, err := repo.ListOverdue(ctx, time.Minute, "", 10, 0)
	require.NoError(t, err)
	require.Empty(t, overdue, "not overdue yet")

	_, err = pool.Exec(ctx, "UPDATE outbox SET sent_at=clock_timestamp()-interval '2 minutes'")
	require.NoError(t, err)
	before := len(pub.Attempts())
	_, err = d.DispatchOnce(ctx, true)
	require.NoError(t, err)
	require.Equal(t, "SENT", messaging.RowStatus(t, pool, releaseID), "the dispatcher never resends a SENT row")
	require.Len(t, pub.Attempts(), before, "and publishes nothing")
	overdue, err = repo.ListOverdue(ctx, time.Minute, "", 10, 0)
	require.NoError(t, err)
	require.Len(t, overdue, 1, "only the SENT release; the CONFIRMED reserve is never overdue")
	require.Equal(t, releaseID, overdue[0].ID)

	requeued, err := repo.Requeue(ctx, releaseID)
	require.NoError(t, err)
	require.Equal(t, "PENDING", requeued.Status)
	require.Equal(t, releaseKey, requeued.IdempotencyKey)

	_, err = d.DispatchOnce(ctx, true)
	require.NoError(t, err)
	results, err = credit.ConsumeFrom(pub)
	require.NoError(t, err)
	require.Len(t, results, 1, "the redelivery returns the lost result")
	require.Equal(t, errand.ResultCreditReleased, results[0].RoutingKey)
	require.Equal(t, releaseKey, results[0].Envelope.CorrelationID)
	require.Equal(t, 10, credit.Balance("auth0|req1"), "released once")

	var releases []string
	for _, m := range pub.MessagesTo(errand.ExchangeCommands) {
		if m.RoutingKey == errand.CommandCreditRelease {
			releases = append(releases, m.Envelope.MessageID)
		}
	}
	require.Equal(t, []string{releaseKey, releaseKey}, releases, "re-sent with the same messageId")
	require.NotEqual(t, reserveKey, releaseKey)

	_, err = repo.ConfirmByKey(ctx, releaseKey)
	require.NoError(t, err)
	require.Equal(t, "CONFIRMED", messaging.RowStatus(t, pool, releaseID))

	// Confirmed rows are never overdue or requeueable, however old they are.
	_, err = pool.Exec(ctx, "UPDATE outbox SET sent_at=clock_timestamp()-interval '1 day', created_at=clock_timestamp()-interval '1 day'")
	require.NoError(t, err)
	overdue, err = repo.ListOverdue(ctx, time.Minute, "", 10, 0)
	require.NoError(t, err)
	require.Empty(t, overdue)
	_, err = repo.Requeue(ctx, releaseID)
	require.ErrorIs(t, err, msgrepo.ErrNotRequeueable)
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
		d := newDispatcher(t, repo, pub, messaging.DispatcherConfig{WorkerID: worker, BatchSize: 3, PollInterval: 5 * time.Millisecond})
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
