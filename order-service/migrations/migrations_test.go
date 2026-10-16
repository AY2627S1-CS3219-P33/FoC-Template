package migrations

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

var tables = []string{
	"errands",
	"errand_transitions",
	"outbox",
	"supplier_fences",
	"errand_reviews",
	"admin_audit",
	"settings",
}

var indexes = []string{
	"errands_open_by_supplier_idx",
	"errands_requester_idx",
	"errands_courier_idx",
	"errands_acceptance_due_idx",
	"errands_pickup_due_idx",
	"errands_delivery_due_idx",
	"errands_verification_due_idx",
	"errands_stuck_reservation_idx",
	"errands_stuck_edit_idx",
	"errand_transitions_errand_idx",
	"errand_transitions_actor_idx",
	"errand_transitions_to_status_idx",
	"errand_transitions_withdrawals_idx",
	"outbox_due_idx",
	"outbox_unconfirmed_credit_idx",
	"supplier_fences_supplier_idx",
	"errand_reviews_open_idx",
}

func TestOrderSchemaMigrationUp(t *testing.T) {
	conn := migratedDatabase(t)
	ctx := context.Background()

	for _, relation := range append(append([]string{}, tables...), indexes...) {
		var found *string
		require.NoError(t, conn.QueryRow(ctx, `SELECT to_regclass($1)::text`, relation).Scan(&found))
		require.NotNilf(t, found, "%s was not created", relation)
	}

	id := insertErrand(t, conn, "OPEN", nil, 10)
	var version int
	require.NoError(t, conn.QueryRow(ctx, `SELECT version FROM errands WHERE id = $1`, id).Scan(&version))
	require.Equal(t, 1, version)
}

func TestOrderSchemaMigrationConstraints(t *testing.T) {
	courier := "courier-1"

	t.Run("errands reject an unknown status", func(t *testing.T) {
		conn := migratedDatabase(t)
		_, err := tryInsertErrand(conn, "LOST", nil, 10)
		requirePostgresCode(t, err, "23514")
	})

	t.Run("errands reject a zero reward", func(t *testing.T) {
		conn := migratedDatabase(t)
		_, err := tryInsertErrand(conn, "OPEN", nil, 0)
		requirePostgresCode(t, err, "23514")
	})

	t.Run("courier states require a courier", func(t *testing.T) {
		conn := migratedDatabase(t)
		for _, status := range []string{"ASSIGNED", "PICKED_UP", "DELIVERED", "REVIEW_REQUIRED"} {
			_, err := tryInsertErrand(conn, status, nil, 10)
			requirePostgresCode(t, err, "23514")
			insertErrand(t, conn, status, &courier, 10)
		}
	})

	t.Run("errands reject a version below one and empty items", func(t *testing.T) {
		conn := migratedDatabase(t)
		ctx := context.Background()
		id := insertErrand(t, conn, "OPEN", nil, 10)

		_, err := conn.Exec(ctx, `UPDATE errands SET version = 0 WHERE id = $1`, id)
		requirePostgresCode(t, err, "23514")
		_, err = conn.Exec(ctx, `UPDATE errands SET items = '[]' WHERE id = $1`, id)
		requirePostgresCode(t, err, "23514")
	})

	t.Run("errands reject a blank contact person", func(t *testing.T) {
		conn := migratedDatabase(t)
		ctx := context.Background()
		id := insertErrand(t, conn, "OPEN", nil, 10)

		_, err := conn.Exec(ctx, `UPDATE errands SET contact_name = ' ' WHERE id = $1`, id)
		requirePostgresCode(t, err, "23514")
		_, err = conn.Exec(ctx, `UPDATE errands SET contact_mobile = '' WHERE id = $1`, id)
		requirePostgresCode(t, err, "23514")
	})

	t.Run("pending edit columns are set together", func(t *testing.T) {
		conn := migratedDatabase(t)
		ctx := context.Background()
		id := insertErrand(t, conn, "OPEN", nil, 10)

		_, err := conn.Exec(ctx, `
			UPDATE errands SET pending_edit = '{"reward": 20}' WHERE id = $1
		`, id)
		requirePostgresCode(t, err, "23514")
		_, err = conn.Exec(ctx, `
			UPDATE errands
			SET pending_edit = '{"reward": 20}',
				pending_edit_key = id::text || ':adjust:1',
				pending_edit_at = clock_timestamp()
			WHERE id = $1
		`, id)
		require.NoError(t, err)
	})

	t.Run("create idempotency key is unique per requester", func(t *testing.T) {
		conn := migratedDatabase(t)
		ctx := context.Background()
		_, err := conn.Exec(ctx, errandInsertSQL, newUUID(t), "OPEN", nil, 10, "requester-1", "create-1")
		require.NoError(t, err)

		_, err = conn.Exec(ctx, errandInsertSQL, newUUID(t), "OPEN", nil, 10, "requester-1", "create-1")
		requirePostgresCode(t, err, "23505")
		_, err = conn.Exec(ctx, errandInsertSQL, newUUID(t), "OPEN", nil, 10, "requester-2", "create-1")
		require.NoError(t, err)
	})

	t.Run("outbox idempotency key is unique", func(t *testing.T) {
		conn := migratedDatabase(t)
		id := insertErrand(t, conn, "PENDING_RESERVATION", nil, 10)

		require.NoError(t, insertOutbox(conn, id, "credit.reserve", id+":reserve", "PENDING"))
		err := insertOutbox(conn, id, "credit.reserve", id+":reserve", "PENDING")
		requirePostgresCode(t, err, "23505")
	})

	t.Run("outbox rejects unknown kinds and confirmed events", func(t *testing.T) {
		conn := migratedDatabase(t)
		id := insertErrand(t, conn, "OPEN", nil, 10)

		requirePostgresCode(t, insertOutbox(conn, id, "credit.mint", id+":mint", "PENDING"), "23514")
		requirePostgresCode(t, insertOutbox(conn, id, "event.errand.opened", id+":opened:2", "CONFIRMED"), "23514")
		requirePostgresCode(t, insertOutbox(conn, id, "credit.release", id+":release", "DONE"), "23514")
		require.NoError(t, insertOutbox(conn, id, "credit.release", id+":release", "CONFIRMED"))
	})

	t.Run("enum columns on the other tables reject unknown values", func(t *testing.T) {
		conn := migratedDatabase(t)
		ctx := context.Background()
		id := insertErrand(t, conn, "OPEN", nil, 10)

		_, err := conn.Exec(ctx, `
			INSERT INTO errand_transitions (errand_id, to_status, event, actor_type, errand_version)
			VALUES ($1, 'OPEN', 'ReservationConfirmed', 'ROBOT', 2)
		`, id)
		requirePostgresCode(t, err, "23514")

		_, err = conn.Exec(ctx, `
			INSERT INTO supplier_fences (operation_id, supplier_id, state)
			VALUES ($1, $2, 'CLOSED')
		`, newUUID(t), newUUID(t))
		requirePostgresCode(t, err, "23514")

		_, err = conn.Exec(ctx, `
			INSERT INTO errand_reviews (errand_id, reason) VALUES ($1, 'BORED')
		`, id)
		requirePostgresCode(t, err, "23514")

		_, err = conn.Exec(ctx, `
			INSERT INTO errand_reviews (errand_id, reason, resolution)
			VALUES ($1, 'DELIVERY_OVERDUE', 'REFUNDED')
		`, id)
		requirePostgresCode(t, err, "23514")

		_, err = conn.Exec(ctx, `
			INSERT INTO admin_audit (actor_id, action, target_type, target_id, outcome)
			VALUES ('admin-1', 'review.resolve', 'errand', $1, 'MAYBE')
		`, id)
		requirePostgresCode(t, err, "23514")
	})

	t.Run("an errand has at most one open review", func(t *testing.T) {
		conn := migratedDatabase(t)
		ctx := context.Background()
		id := insertErrand(t, conn, "REVIEW_REQUIRED", &courier, 10)
		insertReview := func() error {
			_, err := conn.Exec(ctx, `
				INSERT INTO errand_reviews (errand_id, reason) VALUES ($1, 'COURIER_ISSUE')
			`, id)
			return err
		}

		require.NoError(t, insertReview())
		requirePostgresCode(t, insertReview(), "23505")

		_, err := conn.Exec(ctx, `
			UPDATE errand_reviews
			SET resolution = 'REFUNDED', resolver_id = 'admin-1',
				resolution_reason = 'not delivered', resolved_at = now()
			WHERE errand_id = $1
		`, id)
		require.NoError(t, err)
		require.NoError(t, insertReview())
	})
}

func TestOrderSchemaMigrationDown(t *testing.T) {
	conn := migratedDatabase(t)
	ctx := context.Background()
	down, err := Down()
	require.NoError(t, err)
	_, err = conn.Exec(ctx, down)
	require.NoError(t, err)

	for _, relation := range append(append([]string{}, tables...), indexes...) {
		var found *string
		require.NoError(t, conn.QueryRow(ctx, `SELECT to_regclass($1)::text`, relation).Scan(&found))
		require.Nilf(t, found, "%s remains after rollback", relation)
	}
}

const errandInsertSQL = `
	INSERT INTO errands (
		id,
		status,
		courier_id,
		reward,
		requester_id,
		create_idempotency_key,
		supplier_id,
		supplier_version_id,
		delivery_building_id,
		delivery_building_snapshot,
		delivery_detail,
		items,
		contact_name,
		contact_mobile,
		acceptance_deadline
	) VALUES ($1, $2, $3, $4, $5, $6,
		gen_random_uuid(), gen_random_uuid(), gen_random_uuid(),
		'{"name": "Central Library"}', 'Level 3 reading room',
		'[{"name": "Coffee", "quantity": 1}]',
		'Alex Tan', '+65 9123 4567',
		clock_timestamp() + interval '1 hour')
`

// tryInsertErrand inserts an errand with a fresh requester and idempotency
// key and returns its ID.
func tryInsertErrand(conn *pgx.Conn, status string, courierID *string, reward int) (string, error) {
	id, err := randomUUID()
	if err != nil {
		return "", err
	}
	_, err = conn.Exec(context.Background(), errandInsertSQL,
		id, status, courierID, reward, "requester-"+id, "create-"+id)
	return id, err
}

func insertErrand(t *testing.T, conn *pgx.Conn, status string, courierID *string, reward int) string {
	t.Helper()
	id, err := tryInsertErrand(conn, status, courierID, reward)
	require.NoError(t, err)
	return id
}

func insertOutbox(conn *pgx.Conn, errandID, kind, key, status string) error {
	_, err := conn.Exec(context.Background(), `
		INSERT INTO outbox (kind, idempotency_key, errand_id, payload, status)
		VALUES ($1, $2, $3, '{}', $4)
	`, kind, key, errandID, status)
	return err
}

// migratedDatabase applies every migration to a new, empty schema.
func migratedDatabase(t *testing.T) *pgx.Conn {
	t.Helper()
	conn := emptyDatabase(t)
	up, err := Up()
	require.NoError(t, err)
	_, err = conn.Exec(context.Background(), up)
	require.NoError(t, err)
	return conn
}

// emptyDatabase returns a connection whose search path is a new, empty schema.
func emptyDatabase(t *testing.T) *pgx.Conn {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set; skipping PostgreSQL migration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, databaseURL)
	require.NoError(t, err)

	schema := "order_migration_" + strings.ReplaceAll(newUUID(t), "-", "")
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	_, err = conn.Exec(ctx, "CREATE SCHEMA "+quotedSchema)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, "SET search_path TO "+quotedSchema)
	require.NoError(t, err)

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = conn.Exec(cleanupCtx, "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
		_ = conn.Close(cleanupCtx)
	})

	return conn
}

func requirePostgresCode(t *testing.T, err error, code string) {
	t.Helper()
	require.Error(t, err)
	var postgresError *pgconn.PgError
	require.True(t, errors.As(err, &postgresError), "expected PostgreSQL error, got %T: %v", err, err)
	require.Equal(t, code, postgresError.Code)
}

func newUUID(t *testing.T) string {
	t.Helper()
	id, err := randomUUID()
	require.NoError(t, err)
	return id
}

func randomUUID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32], nil
}
