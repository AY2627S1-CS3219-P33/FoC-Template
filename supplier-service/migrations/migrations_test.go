package migrations

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

//go:embed 00001_create_versioned_suppliers.sql
var versionedSuppliersMigration string

func TestVersionedSuppliersMigrationUp(t *testing.T) {
	conn := migratedDatabase(t)
	ctx := context.Background()

	for _, relation := range []string{
		"suppliers",
		"supplier_versions",
		"supplier_deletion_operations",
		"supplier_seed_provenance",
	} {
		var found *string
		require.NoError(t, conn.QueryRow(ctx, `SELECT to_regclass($1)::text`, relation).Scan(&found))
		require.NotNilf(t, found, "%s was not created", relation)
	}

	var normalized string
	require.NoError(t, conn.QueryRow(
		ctx,
		`SELECT normalize_supplier_name(chr(9) || ' NUS' || chr(9) || '  Co-Op ' || chr(10))`,
	).Scan(&normalized))
	require.Equal(t, "nus co-op", normalized)

	supplierID, versionID := insertSupplier(t, conn, "  NUS   Co-Op  ")
	var storedSupplierID, storedVersionID, storedNormalized string
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT s.supplier_id::text, v.version_id::text, v.normalized_name
		FROM suppliers s
		JOIN supplier_versions v ON v.version_id = s.current_version_id
		WHERE s.supplier_id = $1
	`, supplierID).Scan(&storedSupplierID, &storedVersionID, &storedNormalized))
	require.Equal(t, supplierID, storedSupplierID)
	require.Equal(t, versionID, storedVersionID)
	require.Equal(t, "nus co-op", storedNormalized)
}

func TestVersionedSuppliersMigrationConstraints(t *testing.T) {
	t.Run("live normalized names are unique and deleted names are reusable", func(t *testing.T) {
		conn := migratedDatabase(t)
		ctx := context.Background()
		firstSupplierID, _ := insertSupplier(t, conn, "NUS   Co-Op")

		tx, err := conn.Begin(ctx)
		require.NoError(t, err)
		_, err = tx.Exec(ctx, `
			INSERT INTO suppliers (
				supplier_id, current_version_id, current_normalized_name
			) VALUES ($1, $2, 'nus co-op')
		`, newUUID(t), newUUID(t))
		requirePostgresCode(t, err, "23505")
		require.NoError(t, tx.Rollback(ctx))

		_, err = conn.Exec(ctx, `
			UPDATE suppliers
			SET deleted_at = clock_timestamp(), updated_at = clock_timestamp()
			WHERE supplier_id = $1
		`, firstSupplierID)
		require.NoError(t, err)
		insertSupplier(t, conn, "  nus co-op  ")
	})

	t.Run("current version must belong to the same supplier", func(t *testing.T) {
		conn := migratedDatabase(t)
		ctx := context.Background()
		firstSupplierID, firstVersionID := insertSupplier(t, conn, "First Supplier")
		secondSupplierID, _ := insertSupplier(t, conn, "Second Supplier")

		tx, err := conn.Begin(ctx)
		require.NoError(t, err)
		_, err = tx.Exec(ctx, `
			UPDATE suppliers SET current_version_id = $1, updated_at = clock_timestamp()
			WHERE supplier_id = $2
		`, firstVersionID, secondSupplierID)
		require.NoError(t, err)
		err = tx.Commit(ctx)
		requirePostgresCode(t, err, "23503")

		var currentVersionID string
		require.NoError(t, conn.QueryRow(ctx, `
			SELECT current_version_id::text FROM suppliers WHERE supplier_id = $1
		`, firstSupplierID).Scan(&currentVersionID))
		require.Equal(t, firstVersionID, currentVersionID)
	})

	t.Run("current normalized name must match the current version", func(t *testing.T) {
		conn := migratedDatabase(t)
		ctx := context.Background()
		supplierID, _ := insertSupplier(t, conn, "Original Name")

		tx, err := conn.Begin(ctx)
		require.NoError(t, err)
		_, err = tx.Exec(ctx, `
			UPDATE suppliers
			SET current_normalized_name = 'different name', updated_at = clock_timestamp()
			WHERE supplier_id = $1
		`, supplierID)
		require.NoError(t, err)
		err = tx.Commit(ctx)
		requirePostgresCode(t, err, "23514")
	})

	t.Run("supplier versions are append only", func(t *testing.T) {
		conn := migratedDatabase(t)
		ctx := context.Background()
		_, versionID := insertSupplier(t, conn, "Immutable Supplier")

		_, err := conn.Exec(ctx, `
			UPDATE supplier_versions SET building = 'Changed' WHERE version_id = $1
		`, versionID)
		requirePostgresCode(t, err, "55000")
		_, err = conn.Exec(ctx, `DELETE FROM supplier_versions WHERE version_id = $1`, versionID)
		requirePostgresCode(t, err, "55000")
	})

	t.Run("deletion operations constrain leases attempts states and failure codes", func(t *testing.T) {
		conn := migratedDatabase(t)
		ctx := context.Background()
		supplierID, _ := insertSupplier(t, conn, "Deletion Supplier")

		_, err := conn.Exec(ctx, `
			INSERT INTO supplier_deletion_operations (
				operation_id, supplier_id, max_attempts, next_attempt_at
			) VALUES ($1, $2, 5, clock_timestamp())
		`, newUUID(t), supplierID)
		require.NoError(t, err)

		_, err = conn.Exec(ctx, `
			INSERT INTO supplier_deletion_operations (
				operation_id, supplier_id, max_attempts, last_failure_code
			) VALUES ($1, $2, 5, 'raw dependency error')
		`, newUUID(t), supplierID)
		requirePostgresCode(t, err, "23514")

		_, err = conn.Exec(ctx, `
			INSERT INTO supplier_deletion_operations (
				operation_id, supplier_id, max_attempts, claim_id
			) VALUES ($1, $2, 5, $3)
		`, newUUID(t), supplierID, newUUID(t))
		requirePostgresCode(t, err, "23514")

		_, err = conn.Exec(ctx, `
			INSERT INTO supplier_deletion_operations (
				operation_id, supplier_id, max_attempts, attempt_count
			) VALUES ($1, $2, 2, 3)
		`, newUUID(t), supplierID)
		requirePostgresCode(t, err, "23514")

		_, err = conn.Exec(ctx, `
			INSERT INTO supplier_deletion_operations (
				operation_id, supplier_id, state, max_attempts
			) VALUES ($1, $2, 'unknown', 5)
		`, newUUID(t), supplierID)
		requirePostgresCode(t, err, "23514")
	})

	t.Run("seed provenance survives supplier rename and soft deletion", func(t *testing.T) {
		conn := migratedDatabase(t)
		ctx := context.Background()
		supplierID, _ := insertSupplier(t, conn, "Seeded Supplier")
		_, err := conn.Exec(ctx, `
			INSERT INTO supplier_seed_provenance (
				dataset_namespace, source_key, supplier_id
			) VALUES ('template-v1', 'seeded supplier', $1)
		`, supplierID)
		require.NoError(t, err)

		newVersionID := newUUID(t)
		tx, err := conn.Begin(ctx)
		require.NoError(t, err)
		_, err = tx.Exec(ctx, versionInsertSQL,
			newVersionID, supplierID, "Renamed Supplier")
		require.NoError(t, err)
		_, err = tx.Exec(ctx, `
			UPDATE suppliers
			SET current_version_id = $1,
				current_normalized_name = 'renamed supplier',
				updated_at = clock_timestamp()
			WHERE supplier_id = $2
		`, newVersionID, supplierID)
		require.NoError(t, err)
		require.NoError(t, tx.Commit(ctx))

		_, err = conn.Exec(ctx, `
			UPDATE suppliers
			SET deleted_at = clock_timestamp(), updated_at = clock_timestamp()
			WHERE supplier_id = $1
		`, supplierID)
		require.NoError(t, err)

		var provenanceSupplierID string
		require.NoError(t, conn.QueryRow(ctx, `
			SELECT supplier_id::text
			FROM supplier_seed_provenance
			WHERE dataset_namespace = 'template-v1'
			  AND source_key = 'seeded supplier'
		`).Scan(&provenanceSupplierID))
		require.Equal(t, supplierID, provenanceSupplierID)
	})
}

func TestVersionedSuppliersMigrationDown(t *testing.T) {
	conn := migratedDatabase(t)
	ctx := context.Background()
	require.NoError(t, applyMigrationSection(ctx, conn, "Down"))

	for _, relation := range []string{
		"suppliers",
		"supplier_versions",
		"supplier_deletion_operations",
		"supplier_seed_provenance",
	} {
		var found *string
		require.NoError(t, conn.QueryRow(ctx, `SELECT to_regclass($1)::text`, relation).Scan(&found))
		require.Nilf(t, found, "%s remains after rollback", relation)
	}

	var functionFound *string
	require.NoError(t, conn.QueryRow(
		ctx,
		`SELECT to_regprocedure('normalize_supplier_name(text)')::text`,
	).Scan(&functionFound))
	require.Nil(t, functionFound)
}

const versionInsertSQL = `
	INSERT INTO supplier_versions (
		version_id,
		supplier_id,
		name,
		supplier_type,
		building,
		floor,
		location_description,
		latitude,
		longitude,
		opening_time,
		closing_time
	) VALUES ($1, $2, $3, 'shopping', 'Central Library', '1', 'Lobby',
		1.2966, 103.7764, '09:00', '18:00')
`

func insertSupplier(t *testing.T, conn *pgx.Conn, name string) (string, string) {
	t.Helper()
	ctx := context.Background()
	supplierID := newUUID(t)
	versionID := newUUID(t)

	var normalized string
	require.NoError(t, conn.QueryRow(ctx, `SELECT normalize_supplier_name($1)`, name).Scan(&normalized))

	tx, err := conn.Begin(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = tx.Rollback(context.Background())
	})

	_, err = tx.Exec(ctx, `
		INSERT INTO suppliers (
			supplier_id, current_version_id, current_normalized_name
		) VALUES ($1, $2, $3)
	`, supplierID, versionID, normalized)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, versionInsertSQL, versionID, supplierID, name)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))
	return supplierID, versionID
}

func migratedDatabase(t *testing.T) *pgx.Conn {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set; skipping PostgreSQL migration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, databaseURL)
	require.NoError(t, err)

	schema := "supplier_migration_" + strings.ReplaceAll(newUUID(t), "-", "")
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

	require.NoError(t, applyMigrationSection(ctx, conn, "Up"))
	return conn
}

func applyMigrationSection(ctx context.Context, conn *pgx.Conn, section string) error {
	upMarker := "-- +goose Up"
	downMarker := "-- +goose Down"
	upAt := strings.Index(versionedSuppliersMigration, upMarker)
	downAt := strings.Index(versionedSuppliersMigration, downMarker)
	if upAt < 0 || downAt < 0 || downAt <= upAt {
		return fmt.Errorf("migration is missing ordered Goose Up and Down markers")
	}

	var sql string
	switch section {
	case "Up":
		sql = versionedSuppliersMigration[upAt+len(upMarker) : downAt]
	case "Down":
		sql = versionedSuppliersMigration[downAt+len(downMarker):]
	default:
		return fmt.Errorf("unknown migration section %q", section)
	}
	_, err := conn.Exec(ctx, sql)
	return err
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
