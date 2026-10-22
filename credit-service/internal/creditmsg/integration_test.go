package creditmsg_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/creditmsg"
	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/testdb"
	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/wallet"
)

const (
	requester = "00000000-0000-0000-0000-00000000a11c"
	courier   = "00000000-0000-0000-0000-0000000000b0"
)

type fixture struct {
	pool    *pgxpool.Pool
	service *wallet.Service
	handler creditmsg.Handler
}

// newFixture returns the real pieces on an empty database, with auth0|req1 and
// auth0|cou1 holding 100 credits each.
func newFixture(t *testing.T) fixture {
	t.Helper()
	pool := testdb.Pool(t)
	service := wallet.NewService(pool, 100).WithResultRecorder(creditmsg.NewOutboxRecorder())
	for owner, sub := range map[string]string{requester: "auth0|req1", courier: "auth0|cou1"} {
		_, _, err := service.CreateWallet(context.Background(), owner, sub)
		require.NoError(t, err)
	}
	return fixture{pool: pool, service: service, handler: creditmsg.NewHandler(service)}
}

func (f fixture) send(t *testing.T, routingKey, messageID string, version int, data string) error {
	t.Helper()
	body := message(messageID, routingKey, version, data)
	return f.handler.Handle(context.Background(), routingKey, body)
}

func reserveData(amount int) string {
	return fmt.Sprintf(`{"errandId": %q, "errandVersion": 1, "requesterId": "auth0|req1", "amount": %d}`, errand, amount)
}

// storedRow is an outbox row as the tests read it.
type storedRow struct {
	RoutingKey string
	Status     string
	Attempts   int
	Payload    json.RawMessage
}

func (f fixture) outbox(t *testing.T, key string) (storedRow, creditmsg.Envelope) {
	t.Helper()
	row, err := f.outboxRow(key)
	require.NoError(t, err)
	var env creditmsg.Envelope
	require.NoError(t, json.Unmarshal(row.Payload, &env))
	return row, env
}

func (f fixture) outboxRow(key string) (storedRow, error) {
	var r storedRow
	err := f.pool.QueryRow(context.Background(),
		`SELECT routing_key, status, attempts, payload FROM outbox WHERE idempotency_key = $1`, key).
		Scan(&r.RoutingKey, &r.Status, &r.Attempts, &r.Payload)
	return r, err
}

func (f fixture) outboxCount(t *testing.T) int {
	t.Helper()
	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT count(*) FROM outbox`).Scan(&n))
	return n
}

func TestReserveCommandQueuesTheResult(t *testing.T) {
	f := newFixture(t)

	require.NoError(t, f.send(t, "credit.reserve", errand+":reserve", 1, reserveData(30)))

	row, env := f.outbox(t, errand+":reserve:credit.reserved")
	require.Equal(t, "PENDING", row.Status)
	require.Equal(t, "credit.reserved", row.RoutingKey)
	require.Equal(t, errand+":reserve", env.CorrelationID)
	require.Equal(t, errand, env.ErrandID)
	require.EqualValues(t, 1, env.ErrandVersion)
	require.JSONEq(t, `{"amount": 30}`, string(env.Data))

	w, err := f.service.GetByOwner(context.Background(), requester)
	require.NoError(t, err)
	require.EqualValues(t, 70, w.Available)
}

func TestRefusalIsQueuedToo(t *testing.T) {
	f := newFixture(t)

	require.NoError(t, f.send(t, "credit.reserve", errand+":reserve", 1, reserveData(500)))

	_, env := f.outbox(t, errand+":reserve:credit.reservation_refused")
	require.JSONEq(t, `{"code": "INSUFFICIENT_CREDITS"}`, string(env.Data))
}

func TestFullLifecycleQueuesOneResultPerCommand(t *testing.T) {
	f := newFixture(t)
	id := func(suffix string) string { return errand + ":" + suffix }

	require.NoError(t, f.send(t, "credit.reserve", id("reserve"), 1, reserveData(30)))
	require.NoError(t, f.send(t, "credit.adjust", id("adjust:2"), 2, fmt.Sprintf(
		`{"errandId": %q, "errandVersion": 2, "requesterId": "auth0|req1", "newAmount": 40, "seq": 4}`, errand)))
	require.NoError(t, f.send(t, "credit.transfer", id("transfer"), 7, fmt.Sprintf(
		`{"errandId": %q, "errandVersion": 7, "requesterId": "auth0|req1", "courierId": "auth0|cou1", "amount": 40}`, errand)))

	require.Equal(t, 3, f.outboxCount(t))
	_, env := f.outbox(t, id("adjust:2:credit.adjusted"))
	require.JSONEq(t, `{"amount": 40, "seq": 4}`, string(env.Data))
	_, env = f.outbox(t, id("transfer:credit.transferred"))
	require.JSONEq(t, `{}`, string(env.Data))
	require.EqualValues(t, 7, env.ErrandVersion)

	w, err := f.service.GetByOwner(context.Background(), courier)
	require.NoError(t, err)
	require.EqualValues(t, 140, w.Available)
}

func TestRepeatedCommandAddsNoSecondRow(t *testing.T) {
	f := newFixture(t)

	require.NoError(t, f.send(t, "credit.reserve", errand+":reserve", 1, reserveData(30)))
	require.NoError(t, f.send(t, "credit.reserve", errand+":reserve", 1, reserveData(30)))

	require.Equal(t, 1, f.outboxCount(t))
	w, err := f.service.GetByOwner(context.Background(), requester)
	require.NoError(t, err)
	require.EqualValues(t, 70, w.Available)
}

func TestRepeatedCommandSendsASentResultAgain(t *testing.T) {
	f := newFixture(t)
	key := errand + ":reserve:credit.reserved"
	require.NoError(t, f.send(t, "credit.reserve", errand+":reserve", 1, reserveData(30)))
	_, err := f.pool.Exec(context.Background(),
		`UPDATE outbox SET status = 'SENT', attempts = 2, sent_at = now() WHERE idempotency_key = $1`, key)
	require.NoError(t, err)

	// Order-service sent the command again, so it did not get the result.
	require.NoError(t, f.send(t, "credit.reserve", errand+":reserve", 1, reserveData(30)))

	row, _ := f.outbox(t, key)
	require.Equal(t, "PENDING", row.Status)
	require.Zero(t, row.Attempts)
	require.Equal(t, 1, f.outboxCount(t))
}

func TestRepeatedCommandLeavesAPendingRowAlone(t *testing.T) {
	f := newFixture(t)
	key := errand + ":reserve:credit.reserved"
	require.NoError(t, f.send(t, "credit.reserve", errand+":reserve", 1, reserveData(30)))
	_, err := f.pool.Exec(context.Background(),
		`UPDATE outbox SET attempts = 3, lease_owner = 'worker-1' WHERE idempotency_key = $1`, key)
	require.NoError(t, err)

	require.NoError(t, f.send(t, "credit.reserve", errand+":reserve", 1, reserveData(30)))

	var attempts int
	var lease *string
	require.NoError(t, f.pool.QueryRow(context.Background(),
		`SELECT attempts, lease_owner FROM outbox WHERE idempotency_key = $1`, key).Scan(&attempts, &lease))
	require.Equal(t, 3, attempts, "a worker may be publishing this row")
	require.Equal(t, "worker-1", *lease)
}

func TestInvalidCommandQueuesNothing(t *testing.T) {
	f := newFixture(t)

	err := f.send(t, "credit.reserve", errand+":reserve", 1, fmt.Sprintf(
		`{"errandId": %q, "errandVersion": 1, "requesterId": "auth0|req1", "amount": 0}`, errand))

	require.ErrorIs(t, err, wallet.ErrInvalid)
	require.Equal(t, creditmsg.Reject, creditmsg.DispositionOf(err))
	require.Zero(t, f.outboxCount(t))
	var processed int
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT count(*) FROM processed_commands`).Scan(&processed))
	require.Zero(t, processed, "a rejected command leaves no trace, so a fixed one can be applied")
}

type failingRecorder struct{}

func (failingRecorder) RecordResult(context.Context, pgx.Tx, wallet.Reply, wallet.Result) error {
	return errors.New("outbox unavailable")
}

// A balance change and its result commit together: if the result cannot be
// stored, the balance does not change and the command is not marked done.
func TestFailedRecordingRollsTheCommandBack(t *testing.T) {
	f := newFixture(t)
	service := wallet.NewService(f.pool, 100).WithResultRecorder(failingRecorder{})

	err := creditmsg.NewHandler(service).Handle(context.Background(), "credit.reserve",
		message(errand+":reserve", "credit.reserve", 1, reserveData(30)))

	require.Error(t, err)
	require.Equal(t, creditmsg.Requeue, creditmsg.DispositionOf(err))
	w, err := f.service.GetByOwner(context.Background(), requester)
	require.NoError(t, err)
	require.EqualValues(t, 100, w.Available)
	require.EqualValues(t, 0, w.Reserved)

	// The same message succeeds once the outbox works again.
	require.NoError(t, f.send(t, "credit.reserve", errand+":reserve", 1, reserveData(30)))
	require.Equal(t, 1, f.outboxCount(t))
}
