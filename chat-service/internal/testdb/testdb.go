// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-10
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

// Package testdb gives integration tests their own PostgreSQL schema on the
// test database, migrated with the same goose files deployment uses and
// dropped when the test ends. Tests skip themselves when TEST_DATABASE_URL
// is not set; start the database with `docker compose -f compose.test.yaml
// up -d`.
package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/migrations"
)

// Database is one test's schema.
type Database struct {
	// Pool's connections use the test's schema.
	Pool *pgxpool.Pool
	// URL connects to the same schema, for code that opens its own pool.
	URL string
	// Schema is the schema's name.
	Schema string
}

// Empty returns a new schema with no tables.
func Empty(t *testing.T) Database {
	t.Helper()
	baseURL := os.Getenv("TEST_DATABASE_URL")
	if baseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set; skipping PostgreSQL test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	schema := "chat_test_" + randomHex(t)
	quoted := pgx.Identifier{schema}.Sanitize()
	admin, err := pgx.Connect(ctx, baseURL)
	require.NoError(t, err)
	_, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted)
	require.NoError(t, err)
	require.NoError(t, admin.Close(ctx))
	// Cleanups run last-in first-out: the pool closes before the drop.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		conn, err := pgx.Connect(ctx, baseURL)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close(ctx) }()
		_, _ = conn.Exec(ctx, "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
	})

	// pgx sends unknown URL parameters as session settings.
	parsed, err := url.Parse(baseURL)
	require.NoError(t, err)
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	schemaURL := parsed.String()

	pool, err := pgxpool.New(ctx, schemaURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return Database{Pool: pool, URL: schemaURL, Schema: schema}
}

// Migrated returns a new schema with every migration applied.
func Migrated(t *testing.T) Database {
	t.Helper()
	db := Empty(t)
	_, err := Goose(t, db).Up(context.Background())
	require.NoError(t, err)
	return db
}

// Goose returns a goose provider for db's schema, with the embedded
// migrations.
func Goose(t *testing.T, db Database) *goose.Provider {
	t.Helper()
	sqlDB := stdlib.OpenDBFromPool(db.Pool)
	t.Cleanup(func() { _ = sqlDB.Close() })
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS)
	require.NoError(t, err)
	return provider
}

func randomHex(t *testing.T) string {
	t.Helper()
	value := make([]byte, 8)
	_, err := rand.Read(value)
	require.NoError(t, err)
	return hex.EncodeToString(value)
}
