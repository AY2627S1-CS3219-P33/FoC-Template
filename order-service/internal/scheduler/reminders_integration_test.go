package scheduler_test

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/opsrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/scheduler"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/testfakes"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/migrations"
)

// pgReminders is a scheduler with the real opsrepo on a fresh, migrated
// schema. Errand fixtures are written with SQL; the deadline jobs' Store is
// a FakeStore the reminder jobs never touch.
type pgReminders struct {
	conn  *pgx.Conn
	clock *testfakes.FakeClock
	sched *scheduler.Scheduler
	// schema and url let a test open a second connection to the schema.
	schema, url string
}

func newPGReminders(t *testing.T) *pgReminders {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set; skipping PostgreSQL reminder integration test")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	require.NoError(t, err)
	var schema string
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT 'order_scheduler_' || replace(gen_random_uuid()::text, '-', '')`).Scan(&schema))
	quoted := pgx.Identifier{schema}.Sanitize()
	_, err = conn.Exec(ctx, "CREATE SCHEMA "+quoted)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
		_ = conn.Close(context.Background())
	})
	_, err = conn.Exec(ctx, "SET search_path TO "+quoted)
	require.NoError(t, err)
	up, err := migrations.Up()
	require.NoError(t, err)
	_, err = conn.Exec(ctx, up)
	require.NoError(t, err)

	clock := testfakes.NewFakeClock(time.Now().UTC().Truncate(time.Microsecond))
	return &pgReminders{
		conn:  conn,
		clock: clock,
		sched: scheduler.New(testfakes.NewFakeStore(), clock, scheduler.Config{
			Reminders: opsrepo.New(conn),
			Settings:  testfakes.NewFakeSettings(),
			Logger:    slog.New(slog.DiscardHandler),
		}),
		schema: quoted,
		url:    url,
	}
}

// insert writes an errand in status with version and one deadline column.
func (p *pgReminders) insert(t *testing.T, status string, version int, column string, deadline time.Time) string {
	t.Helper()
	var id string
	require.NoError(t, p.conn.QueryRow(context.Background(), `
		INSERT INTO errands (
			status, version, requester_id, courier_id, supplier_id, supplier_version_id,
			delivery_building_id, delivery_building_snapshot, delivery_detail, items,
			contact_name, contact_mobile, reward, acceptance_deadline, create_idempotency_key,
			`+pgx.Identifier{column}.Sanitize()+`
		) VALUES (
			$1, $2, 'requester-1', 'courier-1', gen_random_uuid(), gen_random_uuid(),
			gen_random_uuid(), '{}', 'Room 1', '[{"name": "coffee"}]',
			'Contact', '91234567', 10, $3, gen_random_uuid()::text, $4
		) RETURNING id::text`,
		status, version, p.clock.Now().Add(time.Hour), deadline,
	).Scan(&id))
	return id
}

func (p *pgReminders) reminderKeys(t *testing.T, kind string) []string {
	t.Helper()
	rows, err := p.conn.Query(context.Background(),
		`SELECT idempotency_key FROM outbox WHERE kind = $1 ORDER BY id`, kind)
	require.NoError(t, err)
	keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	return keys
}

func (p *pgReminders) run(t *testing.T, job string) scheduler.BatchResult {
	t.Helper()
	res, err := p.sched.RunOnce(context.Background(), job)
	require.NoError(t, err)
	return res
}

func TestPostgresVerificationReminderOncePerVersion(t *testing.T) {
	p := newPGReminders(t)
	ctx := context.Background()
	kind := errand.ErrandVerificationReminder.Kind()
	id := p.insert(t, "DELIVERED", 5, "verification_deadline", p.clock.Now().Add(25*time.Minute))

	for range 4 {
		p.run(t, scheduler.JobVerificationReminder)
	}
	assert.Equal(t, []string{id + ":verify-reminder:5"}, p.reminderKeys(t, kind), "emitted once")

	var payload map[string]any
	require.NoError(t, p.conn.QueryRow(ctx, `SELECT payload FROM outbox WHERE errand_id = $1`, id).Scan(&payload))
	assert.Equal(t, id, payload["errandId"])
	assert.EqualValues(t, 5, payload["errandVersion"])
	assert.Equal(t, "DELIVERED", payload["status"])
	assert.Equal(t, "requester-1", payload["requesterId"])
	assert.Equal(t, "courier-1", payload["courierId"])
	assert.Contains(t, payload, "deadline")
	assert.Contains(t, payload, "at")

	var status string
	var version int
	require.NoError(t, p.conn.QueryRow(ctx, `SELECT status, version FROM errands WHERE id = $1`, id).Scan(&status, &version))
	assert.Equal(t, "DELIVERED", status, "reminders change no state")
	assert.Equal(t, 5, version)
}

func TestPostgresPickupReminderAgainAfterReacceptance(t *testing.T) {
	p := newPGReminders(t)
	kind := errand.ErrandPickupDeadlineApproaching.Kind()
	id := p.insert(t, "ASSIGNED", 3, "pickup_deadline", p.clock.Now().Add(9*time.Minute))

	p.run(t, scheduler.JobPickupReminder)
	p.run(t, scheduler.JobPickupReminder)

	// Withdrawn and accepted again: version 5, a new pickup deadline.
	_, err := p.conn.Exec(context.Background(),
		`UPDATE errands SET version = 5, pickup_deadline = $2 WHERE id = $1`, id, p.clock.Now().Add(8*time.Minute))
	require.NoError(t, err)
	p.run(t, scheduler.JobPickupReminder)
	p.run(t, scheduler.JobPickupReminder)

	assert.Equal(t, []string{id + ":pickup-reminder:3", id + ":pickup-reminder:5"}, p.reminderKeys(t, kind))
}

func TestPostgresNoReminderAfterTheErrandLeftTheState(t *testing.T) {
	p := newPGReminders(t)
	kind := errand.ErrandVerificationReminder.Kind()
	id := p.insert(t, "DELIVERED", 5, "verification_deadline", p.clock.Now().Add(20*time.Minute))
	_, err := p.conn.Exec(context.Background(), `UPDATE errands SET status = 'COMPLETED', version = 6 WHERE id = $1`, id)
	require.NoError(t, err)

	assert.Equal(t, scheduler.BatchResult{}, p.run(t, scheduler.JobVerificationReminder))
	assert.Empty(t, p.reminderKeys(t, kind))
}

// TestPostgresTransitionInProgressWinsOverTheReminder holds a transition's
// row lock while the job runs: the candidate is selected from the committed
// DELIVERED row, the insert waits for the lock, and once the transition
// commits no reminder is written.
func TestPostgresTransitionInProgressWinsOverTheReminder(t *testing.T) {
	p := newPGReminders(t)
	ctx := context.Background()
	kind := errand.ErrandVerificationReminder.Kind()
	id := p.insert(t, "DELIVERED", 5, "verification_deadline", p.clock.Now().Add(20*time.Minute))

	other, err := pgx.Connect(ctx, p.url)
	require.NoError(t, err)
	defer other.Close(ctx)
	_, err = other.Exec(ctx, "SET search_path TO "+p.schema)
	require.NoError(t, err)
	tx, err := other.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `UPDATE errands SET status = 'COMPLETED', version = 6 WHERE id = $1`, id)
	require.NoError(t, err)

	done := make(chan scheduler.BatchResult, 1)
	go func() {
		res, _ := p.sched.RunOnce(ctx, scheduler.JobVerificationReminder)
		done <- res
	}()
	select {
	case <-done:
		t.Fatal("the reminder insert did not wait for the transition's row lock")
	case <-time.After(200 * time.Millisecond):
	}
	require.NoError(t, tx.Commit(ctx))

	select {
	case res := <-done:
		assert.Equal(t, scheduler.BatchResult{Seen: 1, Applied: 0}, res)
	case <-time.After(5 * time.Second):
		t.Fatal("the job did not finish after the transition committed")
	}
	assert.Empty(t, p.reminderKeys(t, kind))
}
