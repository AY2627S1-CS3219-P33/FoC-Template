package messaging

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/msgrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/require"
)

func testDatabase(t *testing.T) (*pgxpool.Pool, *msgrepo.Repository) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, url)
	require.NoError(t, err)
	schema := pgx.Identifier{"order_messaging_" + strings.ToLower(rand.Text())}.Sanitize()
	_, err = admin.Exec(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		_ = admin.Close(ctx)
	})
	_, err = admin.Exec(ctx, "SET search_path TO "+schema)
	require.NoError(t, err)
	sql, err := migrations.Up()
	require.NoError(t, err)
	_, err = admin.Exec(ctx, sql)
	require.NoError(t, err)
	cfg, err := pgxpool.ParseConfig(url)
	require.NoError(t, err)
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool, msgrepo.New(pool)
}

func insertMessage(t *testing.T, pool *pgxpool.Pool, kind string) (int64, string) {
	t.Helper()
	ctx := context.Background()
	var id string
	err := pool.QueryRow(ctx, `INSERT INTO errands(status,requester_id,supplier_id,supplier_version_id,delivery_building_id,delivery_building_snapshot,delivery_detail,items,contact_name,contact_mobile,reward,acceptance_deadline,create_idempotency_key)
 VALUES('PENDING_RESERVATION','auth0|req1',gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),'{}','lobby','[{"description":"coffee"}]','Alex','91234567',5,clock_timestamp()+interval '1 hour',gen_random_uuid()::text) RETURNING id::text`).Scan(&id)
	require.NoError(t, err)
	var payload any = errand.CreditCommand{ErrandID: id, ErrandVersion: 1, RequesterID: "auth0|req1", Amount: 5}
	if strings.HasPrefix(kind, "event.") {
		payload = errand.EventData{ErrandID: id, ErrandVersion: 1, Event: errand.EventSubmit, To: errand.StatusPendingReservation, ActorType: errand.ActorRequester, ActorID: "auth0|req1", RequesterID: "auth0|req1", Reward: 5, At: time.Now().UTC()}
	}
	data, err := json.Marshal(payload)
	require.NoError(t, err)
	key := id + ":" + kind
	var rowID int64
	err = pool.QueryRow(ctx, `INSERT INTO outbox(kind,idempotency_key,errand_id,payload) VALUES($1,$2,$3,$4) RETURNING id`, kind, key, id, data).Scan(&rowID)
	require.NoError(t, err)
	return rowID, key
}

// insertCommand adds a credit command for the errand of an existing row.
func insertCommand(t *testing.T, pool *pgxpool.Pool, sibling int64, kind string, cmd errand.CreditCommand) (int64, string) {
	t.Helper()
	ctx := context.Background()
	var errandID string
	require.NoError(t, pool.QueryRow(ctx, "SELECT errand_id::text FROM outbox WHERE id=$1", sibling).Scan(&errandID))
	cmd.ErrandID = errandID
	data, err := json.Marshal(cmd)
	require.NoError(t, err)
	key := errandID + ":" + kind
	var id int64
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO outbox(kind,idempotency_key,errand_id,payload) VALUES($1,$2,$3,$4) RETURNING id`, kind, key, errandID, data).Scan(&id))
	return id, key
}

func rowStatus(t *testing.T, pool *pgxpool.Pool, id int64) string {
	t.Helper()
	var status string
	require.NoError(t, pool.QueryRow(context.Background(), "SELECT status FROM outbox WHERE id=$1", id).Scan(&status))
	return status
}

func testBroker(t *testing.T) (string, *amqp.Connection, *amqp.Channel) {
	t.Helper()
	url := os.Getenv("TEST_AMQP_URL")
	if url == "" {
		t.Skip("TEST_AMQP_URL is not set")
	}
	conn, err := amqp.Dial(url)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.CloseDeadline(time.Now().Add(time.Second)) })
	ch, err := conn.Channel()
	require.NoError(t, err)
	require.NoError(t, declareTopology(ch, false))
	return url, conn, ch
}

func eventQueue(t *testing.T, ch *amqp.Channel, key string) string {
	t.Helper()
	q, err := ch.QueueDeclare("", false, true, true, false, nil)
	require.NoError(t, err)
	require.NoError(t, ch.QueueBind(q.Name, key, errand.ExchangeEvents, false, nil))
	return q.Name
}

func TestPublisherDeliversPersistentEnvelopeAndReconnects(t *testing.T) {
	url, _, ch := testBroker(t)
	queue := eventQueue(t, ch, string(errand.ErrandOpened))
	p, err := NewPublisher(url, false)
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })
	row := testRow(errand.ErrandOpened.Kind())
	row.Payload, _ = json.Marshal(errand.EventData{ErrandID: row.ErrandID, ErrandVersion: 2, At: row.CreatedAt})
	m, err := MessageFromOutbox(row)
	require.NoError(t, err)
	for attempt := range 2 {
		if attempt == 1 {
			require.NoError(t, p.socket.Close())
		}
		// A lost connection is retried by the dispatcher, with the same message ID.
		require.Eventually(t, func() bool {
			err := p.Publish(context.Background(), m)
			require.NotErrorIs(t, err, errand.ErrUnpublishable, "a closed connection is retryable")
			return err == nil
		}, 15*time.Second, 50*time.Millisecond)
		delivery, ok, err := ch.Get(queue, true)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, uint8(amqp.Persistent), delivery.DeliveryMode)
		require.Equal(t, m.Envelope.MessageID, delivery.MessageId)
		require.Equal(t, m.RoutingKey, delivery.Type)
		require.Equal(t, "application/json", delivery.ContentType)
		var envelope errand.Envelope
		require.NoError(t, json.Unmarshal(delivery.Body, &envelope))
		require.Equal(t, m.Envelope, envelope)
	}
}

func TestPublisherDeclaresEventTap(t *testing.T) {
	url, _, ch := testBroker(t)
	p, err := NewPublisher(url, true)
	require.NoError(t, err)
	defer p.Close()
	require.NoError(t, p.Ping(context.Background()), "ping connects and declares the topology")
	_, err = ch.QueuePurge(errand.QueueEventsTap, false)
	require.NoError(t, err)
	row := testRow(errand.ErrandOpened.Kind())
	row.Payload, _ = json.Marshal(errand.EventData{ErrandID: row.ErrandID, ErrandVersion: 2, At: row.CreatedAt})
	m, err := MessageFromOutbox(row)
	require.NoError(t, err)
	require.NoError(t, p.Publish(context.Background(), m))
	delivery, ok, err := ch.Get(errand.QueueEventsTap, true)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, m.Envelope.MessageID, delivery.MessageId)
	require.Equal(t, string(errand.ErrandOpened), delivery.RoutingKey)
}

// Publishing is at-least-once: a row published twice (for example after a
// crash before MarkSent) arrives twice with the same MessageId, and consumers
// deduplicate by it.
func TestPublisherRepublishKeepsMessageID(t *testing.T) {
	url, _, ch := testBroker(t)
	q, err := ch.QueueDeclare("", false, true, true, false, nil)
	require.NoError(t, err)
	require.NoError(t, ch.QueueBind(q.Name, errand.CommandCreditRelease, errand.ExchangeCommands, false, nil))
	p, err := NewPublisher(url, false)
	require.NoError(t, err)
	defer p.Close()
	m, err := MessageFromOutbox(testRow(errand.KindCreditRelease))
	require.NoError(t, err)
	for range 2 {
		require.NoError(t, p.Publish(context.Background(), m))
	}
	for range 2 {
		delivery, ok, err := ch.Get(q.Name, true)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, m.Envelope.MessageID, delivery.MessageId)
		require.Equal(t, errand.CommandCreditRelease, delivery.Type)
	}
}

func TestPublisherRejectsUnroutableMessages(t *testing.T) {
	url, _, _ := testBroker(t)
	p, err := NewPublisher(url, false)
	require.NoError(t, err)
	defer p.Close()
	// No event queue binds this routing key in this test broker.
	m, err := MessageFromOutbox(testRow(errand.KindCreditReserve))
	require.NoError(t, err)
	// Use a fresh virtual host in CI/local tests; the adapter never declares the
	// credit-service command queue, so this is unroutable until credit owns it.
	require.ErrorIs(t, p.Publish(context.Background(), m), ErrUnroutable)
}

func TestDispatcherPublishesDatabaseOutboxToRabbitMQ(t *testing.T) {
	url, _, ch := testBroker(t)
	pool, repo := testDatabase(t)
	queue := eventQueue(t, ch, string(errand.ErrandPlaced))
	p, err := NewPublisher(url, false)
	require.NoError(t, err)
	defer p.Close()
	id, key := insertMessage(t, pool, errand.ErrandPlaced.Kind())
	d := newTestDispatcher(t, repo, p, DispatcherConfig{WorkerID: "integration", PollInterval: 10 * time.Millisecond})
	_, err = d.dispatch(context.Background(), false)
	require.NoError(t, err)
	require.Equal(t, "SENT", rowStatus(t, pool, id))
	delivery, ok, err := ch.Get(queue, true)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, key, delivery.MessageId)
	var envelope errand.Envelope
	require.NoError(t, json.Unmarshal(delivery.Body, &envelope))
	require.Equal(t, string(errand.ErrandPlaced), envelope.Type)
	resent, err := repo.ResendUnconfirmed(context.Background(), time.Microsecond, 10)
	require.NoError(t, err)
	require.Empty(t, resent, "events do not wait for credit results")
	require.Equal(t, "SENT", rowStatus(t, pool, id))
}

func TestDispatcherPublishesCreditCommandToRabbitMQ(t *testing.T) {
	url, _, ch := testBroker(t)
	pool, repo := testDatabase(t)
	// This test queue stands in for a credit-service-owned consumer.
	q, err := ch.QueueDeclare("", false, true, true, false, nil)
	require.NoError(t, err)
	require.NoError(t, ch.QueueBind(q.Name, errand.CommandCreditReserve, errand.ExchangeCommands, false, nil))
	p, err := NewPublisher(url, false)
	require.NoError(t, err)
	defer p.Close()
	id, key := insertMessage(t, pool, errand.KindCreditReserve)
	d := newTestDispatcher(t, repo, p, DispatcherConfig{WorkerID: "credit-integration", PollInterval: 10 * time.Millisecond})
	_, err = d.dispatch(context.Background(), true)
	require.NoError(t, err)
	delivery, ok, err := ch.Get(q.Name, true)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, key, delivery.MessageId)
	require.Equal(t, errand.CommandCreditReserve, delivery.RoutingKey)
	require.Equal(t, "SENT", rowStatus(t, pool, id))
	_, err = repo.ConfirmByKey(context.Background(), key)
	require.NoError(t, err)
	require.Equal(t, "CONFIRMED", rowStatus(t, pool, id))
}

func TestDispatcherRunProcessesBothLoopsAndStops(t *testing.T) {
	url, _, ch := testBroker(t)
	pool, repo := testDatabase(t)
	queue := eventQueue(t, ch, string(errand.ErrandPlaced))
	require.NoError(t, ch.QueueBind(queue, errand.CommandCreditReserve, errand.ExchangeCommands, false, nil))
	p, err := NewPublisher(url, false)
	require.NoError(t, err)
	defer p.Close()
	creditID, _ := insertMessage(t, pool, errand.KindCreditReserve)
	eventID, _ := insertMessage(t, pool, errand.ErrandPlaced.Kind())
	d := newTestDispatcher(t, repo, p, DispatcherConfig{WorkerID: "run-integration", PollInterval: 10 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	require.Eventually(t, func() bool { return rowStatus(t, pool, creditID) == "SENT" && rowStatus(t, pool, eventID) == "SENT" }, 10*time.Second, 20*time.Millisecond)
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("dispatcher did not stop")
	}
}
