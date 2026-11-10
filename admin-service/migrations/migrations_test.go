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

var tables = []string{"transition_facts", "errand_facts"}

var indexes = []string{
	"transition_facts_type_at_idx",
	"transition_facts_errand_idx",
	"errand_facts_opened_idx",
	"errand_facts_accepted_idx",
	"errand_facts_terminal_idx",
	"errand_facts_enrichment_due_idx",
}

func TestAdminSchemaMigrationUp(t *testing.T) {
	conn := migratedDatabase(t)
	ctx := context.Background()

	for _, relation := range append(append([]string{}, tables...), indexes...) {
		var found *string
		require.NoError(t, conn.QueryRow(ctx, `SELECT to_regclass($1)::text`, relation).Scan(&found))
		require.NotNilf(t, found, "%s was not created", relation)
	}
}

func TestAdminSchemaMigrationDown(t *testing.T) {
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

func TestAdminSchemaMigrationIsOrdered(t *testing.T) {
	names, err := Names()
	require.NoError(t, err)
	require.Equal(t, []string{"00001_create_admin_schema.sql"}, names)
	for _, name := range names {
		for _, section := range []string{"Up", "Down"} {
			_, err := Section(name, section)
			require.NoErrorf(t, err, "%s %s", name, section)
		}
	}
}

func TestTransitionFactsDedupeByMessageID(t *testing.T) {
	conn := migratedDatabase(t)
	ctx := context.Background()
	errandID := newUUID(t)

	insert := func(messageID string) error {
		_, err := conn.Exec(ctx, `
			INSERT INTO transition_facts (message_id, errand_id, errand_version, event_type, from_status, to_status, reward, occurred_at)
			VALUES ($1, $2, 3, 'errand.accepted', 'OPEN', 'ASSIGNED', 8, now())`, messageID, errandID)
		return err
	}
	require.NoError(t, insert(errandID+":accepted:3"))
	requirePostgresCode(t, insert(errandID+":accepted:3"), "23505")
	require.NoError(t, insert(errandID+":accepted:4"), "another message is another row")
}

func TestTransitionFactsConstraints(t *testing.T) {
	ctx := context.Background()
	for name, row := range map[string]string{
		"unknown to_status":   `'m1', gen_random_uuid(), 1, 'errand.x', NULL, 'LOST', 8`,
		"unknown from_status": `'m2', gen_random_uuid(), 1, 'errand.x', 'LOST', 'OPEN', 8`,
		"zero version":        `'m3', gen_random_uuid(), 0, 'errand.x', NULL, 'OPEN', 8`,
	} {
		t.Run(name, func(t *testing.T) {
			conn := migratedDatabase(t)
			_, err := conn.Exec(ctx, `INSERT INTO transition_facts
				(message_id, errand_id, errand_version, event_type, from_status, to_status, reward, occurred_at)
				VALUES (`+row+`, now())`)
			requirePostgresCode(t, err, "23514")
		})
	}
}

func TestErrandFactsConstraints(t *testing.T) {
	ctx := context.Background()
	insert := func(conn *pgx.Conn, status string, version int, terminalStatus, terminalAt string) error {
		_, err := conn.Exec(ctx, `
			INSERT INTO errand_facts (errand_id, status, version, last_event_at, reward, terminal_status, terminal_at)
			VALUES (gen_random_uuid(), $1, $2, now(), 8, NULLIF($3, ''), NULLIF($4, '')::timestamptz)`,
			status, version, terminalStatus, terminalAt)
		return err
	}

	t.Run("accepts a finished errand", func(t *testing.T) {
		require.NoError(t, insert(migratedDatabase(t), "COMPLETED", 7, "COMPLETED", "2026-03-02T09:00:00Z"))
	})
	t.Run("accepts an open errand", func(t *testing.T) {
		require.NoError(t, insert(migratedDatabase(t), "OPEN", 2, "", ""))
	})
	t.Run("rejects an unknown status", func(t *testing.T) {
		requirePostgresCode(t, insert(migratedDatabase(t), "LOST", 1, "", ""), "23514")
	})
	t.Run("rejects a zero version", func(t *testing.T) {
		requirePostgresCode(t, insert(migratedDatabase(t), "OPEN", 0, "", ""), "23514")
	})
	t.Run("rejects a non-terminal terminal_status", func(t *testing.T) {
		requirePostgresCode(t, insert(migratedDatabase(t), "OPEN", 1, "OPEN", "2026-03-02T09:00:00Z"), "23514")
	})
	t.Run("terminal status and time go together", func(t *testing.T) {
		conn := migratedDatabase(t)
		requirePostgresCode(t, insert(conn, "COMPLETED", 1, "COMPLETED", ""), "23514")
		requirePostgresCode(t, insert(conn, "COMPLETED", 1, "", "2026-03-02T09:00:00Z"), "23514")
	})
	t.Run("errand_id is the primary key", func(t *testing.T) {
		conn := migratedDatabase(t)
		id := newUUID(t)
		insertID := func() error {
			_, err := conn.Exec(ctx, `INSERT INTO errand_facts (errand_id, status, version, last_event_at, reward)
				VALUES ($1, 'OPEN', 1, now(), 8)`, id)
			return err
		}
		require.NoError(t, insertID())
		requirePostgresCode(t, insertID(), "23505")
	})
}

// TestRuntimeRoleGrants checks the privileges the migration gives the runtime
// role: row access and nothing else. It needs a database user allowed to
// create a role; the role is created here only when it does not exist, and
// removed only if this test created it.
func TestRuntimeRoleGrants(t *testing.T) {
	conn := emptyDatabase(t)
	ctx := context.Background()
	var exists bool
	require.NoError(t, conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'admin_service')`).Scan(&exists))
	if !exists {
		if _, err := conn.Exec(ctx, `CREATE ROLE admin_service NOLOGIN`); err != nil {
			t.Skipf("cannot create the admin_service role: %v", err)
		}
		t.Cleanup(func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			// Cleanups run last in, first out, so this runs before the schema
			// is dropped: release the role's grants first.
			_, _ = conn.Exec(cleanup, `DROP OWNED BY admin_service`)
			_, _ = conn.Exec(cleanup, `DROP ROLE IF EXISTS admin_service`)
		})
	}
	up, err := Up()
	require.NoError(t, err)
	_, err = conn.Exec(ctx, up)
	require.NoError(t, err)

	has := func(table, privilege string) bool {
		var ok bool
		require.NoError(t, conn.QueryRow(ctx, `SELECT has_table_privilege('admin_service', $1, $2)`, table, privilege).Scan(&ok))
		return ok
	}
	for _, table := range tables {
		for _, privilege := range []string{"SELECT", "INSERT", "UPDATE"} {
			require.Truef(t, has(table, privilege), "%s on %s", privilege, table)
		}
		for _, privilege := range []string{"DELETE", "TRUNCATE", "REFERENCES", "TRIGGER"} {
			require.Falsef(t, has(table, privilege), "%s on %s", privilege, table)
		}
	}
}

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

	schema := "admin_migration_" + strings.ReplaceAll(newUUID(t), "-", "")
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
	value := make([]byte, 16)
	_, err := rand.Read(value)
	require.NoError(t, err)
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}
