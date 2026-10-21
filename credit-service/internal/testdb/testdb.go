// Package testdb gives integration tests a migrated, empty PostgreSQL
// database. Tests skip themselves when TEST_DATABASE_URL is not set; start the
// database with `docker compose -f compose.test.yaml up -d`.
package testdb

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/migrations"
)

// Pool returns a pool on the test database with every migration applied and
// all credit tables emptied.
func Pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, url)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	require.NoError(t, err)
	_, err = provider.Up(ctx)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `TRUNCATE ledger_entries, reservations, wallets RESTART IDENTITY CASCADE`)
	require.NoError(t, err)
	return pool
}
