package readiness

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestPostgresCheckerAgainstRealDatabase(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set; skipping PostgreSQL readiness integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	recorder, body := serve(t, NewHandler(Postgres(pool.Ping), Redis(nil)))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, readyzBody{Status: "ready", Dependencies: map[string]Status{
		NamePostgres: StatusOK, NameRedis: StatusDisabled}}, body)

	// Nothing listens on port 1, so the pool cannot connect.
	unreachable, err := pgxpool.New(ctx, "postgres://order:s3cret@127.0.0.1:1/order?sslmode=disable&connect_timeout=1")
	require.NoError(t, err)
	t.Cleanup(unreachable.Close)

	recorder, _ = serve(t, NewHandler(Postgres(unreachable.Ping)).WithTimeout(500*time.Millisecond))
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	require.JSONEq(t, `{"status":"not_ready"}`, recorder.Body.String())
}
