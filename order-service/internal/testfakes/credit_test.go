package testfakes_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/testfakes"
)

func command(routingKey, messageID string, cmd errand.CreditCommand) errand.Message {
	data, _ := json.Marshal(cmd)
	return errand.Message{
		Exchange:   errand.ExchangeCommands,
		RoutingKey: routingKey,
		Envelope: errand.Envelope{
			MessageID:     messageID,
			Type:          routingKey,
			OccurredAt:    t0,
			ErrandID:      cmd.ErrandID,
			ErrandVersion: cmd.ErrandVersion,
			Data:          data,
		},
	}
}

func reserve(errandID string, amount int) errand.Message {
	return command(errand.CommandCreditReserve, errand.IdempotencyKey(errandID, "reserve", 0), errand.CreditCommand{
		ErrandID: errandID, ErrandVersion: 1, RequesterID: requesterID, Amount: amount,
	})
}

func adjust(errandID string, newAmount, seq int) errand.Message {
	return command(errand.CommandCreditAdjust, errand.IdempotencyKey(errandID, "adjust", seq), errand.CreditCommand{
		ErrandID: errandID, ErrandVersion: 2, RequesterID: requesterID, NewAmount: newAmount, Seq: seq,
	})
}

func release(errandID string) errand.Message {
	return command(errand.CommandCreditRelease, errand.IdempotencyKey(errandID, "release", 0), errand.CreditCommand{
		ErrandID: errandID, ErrandVersion: 3, RequesterID: requesterID,
	})
}

func resultOf(t *testing.T, m errand.Message) errand.CreditResult {
	t.Helper()
	var r errand.CreditResult
	require.NoError(t, json.Unmarshal(m.Envelope.Data, &r))
	return r
}

func newCredit(balance int) *testfakes.FakeCreditService {
	c := testfakes.NewFakeCreditService(testfakes.NewFakeClock(t0))
	c.SetBalance(requesterID, balance)
	return c
}

func TestFakeCreditServiceReplayChangesNothing(t *testing.T) {
	c := newCredit(100)
	cmd := reserve("e-1", 30)

	first, err := c.Handle(cmd)
	require.NoError(t, err)
	require.Len(t, first, 1)
	assert.Equal(t, errand.ResultCreditReserved, first[0].RoutingKey)
	assert.Equal(t, errand.ExchangeEvents, first[0].Exchange)
	assert.Equal(t, cmd.Envelope.MessageID, first[0].Envelope.CorrelationID)
	assert.Equal(t, 70, c.Balance(requesterID))

	replay, err := c.Handle(cmd)
	require.NoError(t, err)
	assert.Equal(t, first, replay, "the original result is re-published")
	assert.Equal(t, 70, c.Balance(requesterID))
	r, _ := c.Reservation("e-1")
	assert.Equal(t, 30, r.Amount)

	// A replayed release returns the balance once.
	_, err = c.Handle(release("e-1"))
	require.NoError(t, err)
	_, err = c.Handle(release("e-1"))
	require.NoError(t, err)
	assert.Equal(t, 100, c.Balance(requesterID))
}

func TestFakeCreditServiceIgnoresOlderAdjust(t *testing.T) {
	c := newCredit(100)
	_, err := c.Handle(reserve("e-1", 10))
	require.NoError(t, err)

	// The revert (seq 5, back to 10) overtakes the original edit (seq 4, to 40).
	out, err := c.Handle(adjust("e-1", 10, 5))
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, errand.ResultCreditAdjusted, out[0].RoutingKey)

	out, err = c.Handle(adjust("e-1", 40, 4))
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, errand.ResultCreditAdjusted, out[0].RoutingKey, "acknowledged")
	assert.Equal(t, errand.CreditResult{Amount: 10, Seq: 4}, resultOf(t, out[0]), "with the current amount")

	r, _ := c.Reservation("e-1")
	assert.Equal(t, 10, r.Amount)
	assert.Equal(t, 5, r.LastSeq)
	assert.Equal(t, 90, c.Balance(requesterID))

	// A newer adjust still applies.
	out, err = c.Handle(adjust("e-1", 25, 6))
	require.NoError(t, err)
	assert.Equal(t, errand.CreditResult{Amount: 25, Seq: 6}, resultOf(t, out[0]))
	assert.Equal(t, 75, c.Balance(requesterID))
}

func TestFakeCreditServiceRules(t *testing.T) {
	t.Run("insufficient balance refuses the reserve", func(t *testing.T) {
		c := newCredit(5)
		out, err := c.Handle(reserve("e-1", 10))
		require.NoError(t, err)
		assert.Equal(t, errand.ResultCreditReservationRefused, out[0].RoutingKey)
		assert.Equal(t, errand.CreditCodeInsufficientCredits, resultOf(t, out[0]).Code)
		assert.Equal(t, 5, c.Balance(requesterID))
	})

	t.Run("a release before the reserve leaves a tombstone", func(t *testing.T) {
		c := newCredit(100)
		out, err := c.Handle(release("e-1"))
		require.NoError(t, err)
		assert.Equal(t, errand.ResultCreditReleased, out[0].RoutingKey)
		assert.True(t, c.Tombstoned("e-1"))

		out, err = c.Handle(reserve("e-1", 10))
		require.NoError(t, err)
		assert.Equal(t, errand.ResultCreditReservationRefused, out[0].RoutingKey)
		assert.Equal(t, errand.CreditCodeReservationReleased, resultOf(t, out[0]).Code)
		assert.Equal(t, 100, c.Balance(requesterID))
	})

	t.Run("transfer pays the courier; release after transfer fails", func(t *testing.T) {
		c := newCredit(100)
		_, err := c.Handle(reserve("e-1", 10))
		require.NoError(t, err)
		out, err := c.Handle(command(errand.CommandCreditTransfer, "e-1:transfer", errand.CreditCommand{
			ErrandID: "e-1", RequesterID: requesterID, CourierID: "courier-a", Amount: 10,
		}))
		require.NoError(t, err)
		assert.Equal(t, errand.ResultCreditTransferred, out[0].RoutingKey)
		assert.Equal(t, 10, c.Balance("courier-a"))

		out, err = c.Handle(release("e-1"))
		require.NoError(t, err)
		assert.Equal(t, errand.ResultCreditOperationFailed, out[0].RoutingKey)
		assert.Equal(t, errand.CreditCodeReservationAlreadyTransferred, resultOf(t, out[0]).Code)
	})
}

func TestFakeCreditServiceScripts(t *testing.T) {
	t.Run("no reply, then the redelivery returns the lost result", func(t *testing.T) {
		c := newCredit(100)
		c.Script(errand.CommandCreditReserve, testfakes.CreditScript{Outcome: testfakes.CreditNoReply})
		out, err := c.Handle(reserve("e-1", 10))
		require.NoError(t, err)
		assert.Empty(t, out)
		assert.Equal(t, 90, c.Balance(requesterID), "processed even though the reply was lost")

		out, err = c.Handle(reserve("e-1", 10))
		require.NoError(t, err)
		require.Len(t, out, 1)
		assert.Equal(t, errand.ResultCreditReserved, out[0].RoutingKey)
		assert.Equal(t, 90, c.Balance(requesterID))
	})

	t.Run("duplicate reply", func(t *testing.T) {
		c := newCredit(100)
		c.Script(errand.CommandCreditReserve, testfakes.CreditScript{Outcome: testfakes.CreditDuplicateReply})
		out, err := c.Handle(reserve("e-1", 10))
		require.NoError(t, err)
		require.Len(t, out, 2)
		assert.Equal(t, out[0], out[1])
		assert.Equal(t, 90, c.Balance(requesterID))
	})

	t.Run("refused and operation_failed change nothing", func(t *testing.T) {
		c := newCredit(100)
		c.Script(errand.CommandCreditReserve, testfakes.CreditScript{Outcome: testfakes.CreditRefuse, Code: errand.CreditCodeAccountNotFound})
		out, err := c.Handle(reserve("e-1", 10))
		require.NoError(t, err)
		assert.Equal(t, errand.ResultCreditReservationRefused, out[0].RoutingKey)
		assert.Equal(t, errand.CreditCodeAccountNotFound, resultOf(t, out[0]).Code)

		c.Script(errand.CommandCreditReserve, testfakes.CreditScript{Outcome: testfakes.CreditOperationFailed})
		out, err = c.Handle(reserve("e-2", 10))
		require.NoError(t, err)
		assert.Equal(t, errand.ResultCreditOperationFailed, out[0].RoutingKey)
		assert.Equal(t, 100, c.Balance(requesterID))

		// Scripts are used up; the next command is applied normally.
		out, err = c.Handle(reserve("e-3", 10))
		require.NoError(t, err)
		assert.Equal(t, errand.ResultCreditReserved, out[0].RoutingKey)
	})
}

// The fakes compose: store rows → publisher → credit service → results.
func TestFakeCreditServiceConsumesPublishedRows(t *testing.T) {
	ctx := context.Background()
	s := testfakes.NewFakeStore()
	pub := testfakes.NewFakePublisher()
	c := newCredit(100)

	_, err := s.Submit(ctx, newErrand("e-1", t0), t0)
	require.NoError(t, err)
	for _, row := range s.Outbox() {
		m, err := testfakes.MessageFromOutbox(row)
		require.NoError(t, err)
		require.NoError(t, pub.Publish(ctx, m))
	}
	require.Len(t, pub.MessagesTo(errand.ExchangeEvents), 1, "errand.placed")

	c.SetDown(true)
	results, err := c.ConsumeFrom(pub)
	require.NoError(t, err)
	assert.Empty(t, results, "commands wait while credit-service is down")

	c.SetDown(false)
	results, err = c.ConsumeFrom(pub)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, errand.ResultCreditReserved, results[0].RoutingKey)
	assert.Equal(t, "e-1:reserve", results[0].Envelope.CorrelationID)
	assert.Equal(t, reward, resultOf(t, results[0]).Amount)
	assert.Equal(t, 100-reward, c.Balance(requesterID))

	results, err = c.ConsumeFrom(pub)
	require.NoError(t, err)
	assert.Empty(t, results, "each message is consumed once")
}

func TestFakePublisherFaults(t *testing.T) {
	ctx := context.Background()
	pub := testfakes.NewFakePublisher()
	m := reserve("e-1", 10)
	pub.FailNext("Publish", assert.AnError)
	require.ErrorIs(t, pub.Publish(ctx, m), assert.AnError)
	require.NoError(t, pub.Publish(ctx, m))
	assert.Len(t, pub.Messages(), 1)
	assert.Len(t, pub.Attempts(), 2)
}

func TestFakeOpenErrandCacheExpires(t *testing.T) {
	ctx := context.Background()
	clock := testfakes.NewFakeClock(t0)
	cache := testfakes.NewFakeOpenErrandCache(clock)
	_, ok := cache.Get(ctx)
	assert.False(t, ok)

	cache.Set(ctx, []errand.OpenErrandView{{ErrandID: "e-1"}}, 2*time.Second)
	views, ok := cache.Get(ctx)
	require.True(t, ok)
	assert.Equal(t, "e-1", views[0].ErrandID)

	cache.FailNext("Get", assert.AnError)
	_, ok = cache.Get(ctx)
	assert.False(t, ok, "an error is a miss")

	clock.Advance(2 * time.Second)
	_, ok = cache.Get(ctx)
	assert.False(t, ok)
	assert.Len(t, cache.CallsTo("Get"), 4)
}
