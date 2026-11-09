// Package testdb gives database tests a private, migrated schema in the
// database named by TEST_DATABASE_URL. Tests skip when it is not set.
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
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/migrations"
)

// DB is one test's schema.
type DB struct {
	// Conn is a connection whose search path is the schema, for fixtures and
	// assertions. It is not safe for concurrent use.
	Conn *pgx.Conn
	// Schema is the schema's name.
	Schema string
	// URL is TEST_DATABASE_URL with the schema as search path, for code that
	// opens its own connections.
	URL string
}

// Migrated creates a schema with every migration applied and drops it when
// the test ends.
func Migrated(t *testing.T) DB {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set; skipping PostgreSQL integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, databaseURL)
	require.NoError(t, err)
	suffix := make([]byte, 8)
	_, err = rand.Read(suffix)
	require.NoError(t, err)
	schema := "admin_test_" + hex.EncodeToString(suffix)
	_, err = conn.Exec(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = conn.Exec(cleanup, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
		_ = conn.Close(cleanup)
	})
	_, err = conn.Exec(ctx, "SET search_path TO "+schema)
	require.NoError(t, err)
	up, err := migrations.Up()
	require.NoError(t, err)
	_, err = conn.Exec(ctx, up)
	require.NoError(t, err)

	parsed, err := url.Parse(databaseURL)
	require.NoError(t, err)
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return DB{Conn: conn, Schema: schema, URL: parsed.String()}
}

// Pool opens a pool on the schema, closed when the test ends.
func (d DB) Pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), d.URL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}
