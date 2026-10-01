package supplierrepo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
)

// TestPersistenceSurvivesDatabaseAndServiceRestart is opt-in because it
// restarts the disposable Compose database. Run it alone as documented in the
// README; constructing a new pool and repository represents the service
// process coming back after PostgreSQL is available again.
func TestPersistenceSurvivesDatabaseAndServiceRestart(t *testing.T) {
	if os.Getenv("RUN_DATABASE_RESTART_TEST") != "1" {
		t.Skip("set RUN_DATABASE_RESTART_TEST=1 to run the exclusive database restart test")
	}
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("TEST_DATABASE_URL is required for the database restart test")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatal("docker is required for the database restart test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, databaseURL)
	require.NoError(t, err)
	schema := "supplier_restart_" + strings.ReplaceAll(testUUID(t), "-", "")
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	_, err = admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		cleanup, cleanupErr := pgx.Connect(cleanupCtx, databaseURL)
		if cleanupErr == nil {
			_, _ = cleanup.Exec(cleanupCtx, "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
			_ = cleanup.Close(cleanupCtx)
		}
	})

	_, sourceFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	migrationPath := filepath.Join(filepath.Dir(sourceFile), "..", "..", "..", "migrations", "00001_create_versioned_suppliers.sql")
	migration, err := os.ReadFile(migrationPath)
	require.NoError(t, err)
	upSQL, err := migrationUp(string(migration))
	require.NoError(t, err)

	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	require.NoError(t, err)
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, upSQL)
	require.NoError(t, err)

	repository := New(pool)
	createdAt := time.Now().UTC()
	created, err := repository.Create(ctx, validDetails("Restart Supplier"), createdAt)
	require.NoError(t, err)
	renamed := "Restart Supplier Updated"
	updated, err := repository.Update(ctx, created.SupplierID, supplier.Patch{Name: &renamed}, createdAt.Add(time.Minute))
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO supplier_seed_provenance (dataset_namespace, source_key, supplier_id, seeded_at)
		VALUES ('template-v1', 'restart supplier', $1, $2)
	`, created.SupplierID, createdAt)
	require.NoError(t, err)
	operationID := supplier.DeletionOperationID(testUUID(t))
	_, err = repository.CreateOrGetDeletion(ctx, operationID, created.SupplierID, 3, createdAt.Add(2*time.Minute))
	require.NoError(t, err)

	pool.Close()
	require.NoError(t, admin.Close(ctx))

	composePath := filepath.Join(filepath.Dir(sourceFile), "..", "..", "..", "compose.test.yaml")
	restartCtx, restartCancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer restartCancel()
	command := exec.CommandContext(restartCtx, "docker", "compose", "-f", composePath, "restart", "supplier-test-db")
	if output, restartErr := command.CombinedOutput(); restartErr != nil {
		t.Fatalf("restart disposable PostgreSQL: %v: %s", restartErr, strings.TrimSpace(string(output)))
	}

	restartedPool := waitForRestartedPool(t, poolConfig)
	t.Cleanup(restartedPool.Close)

	restartedRepository := New(restartedPool)
	originalVersion, err := restartedRepository.GetVersion(context.Background(), created.VersionID)
	require.NoError(t, err)
	require.Equal(t, created.Details, originalVersion.Details)
	currentVersion, err := restartedRepository.GetVersion(context.Background(), updated.VersionID)
	require.NoError(t, err)
	require.Equal(t, updated.Details, currentVersion.Details)

	var provenanceSupplierID string
	err = restartedPool.QueryRow(context.Background(), `
		SELECT supplier_id::text
		FROM supplier_seed_provenance
		WHERE dataset_namespace = 'template-v1' AND source_key = 'restart supplier'
	`).Scan(&provenanceSupplierID)
	require.NoError(t, err)
	require.Equal(t, string(created.SupplierID), provenanceSupplierID)

	pending, err := restartedRepository.GetDeletion(context.Background(), operationID)
	require.NoError(t, err)
	require.Equal(t, supplier.DeletionRequested, pending.State)
	require.Equal(t, created.SupplierID, pending.SupplierID)
}

func waitForRestartedPool(t *testing.T, config *pgxpool.Config) *pgxpool.Pool {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		pool, err := pgxpool.NewWithConfig(context.Background(), config.Copy())
		if err == nil {
			pingCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			err = pool.Ping(pingCtx)
			cancel()
			if err == nil {
				return pool
			}
			pool.Close()
		}
		if time.Now().After(deadline) {
			t.Fatalf("PostgreSQL did not become ready after restart: %v", err)
		}
		time.Sleep(250 * time.Millisecond)
	}
}
