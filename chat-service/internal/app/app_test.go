package app

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/config"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/testdb"
)

func testConfig(databaseURL string) config.Config {
	return config.Config{
		Environment:     config.Test,
		HTTP:            config.HTTPConfig{Host: "127.0.0.1", Port: 0},
		LogLevel:        "info",
		StartupTimeout:  3 * time.Second,
		ShutdownTimeout: 3 * time.Second,
		Database:        config.DatabaseConfig{URL: databaseURL, MinConnections: 0, MaxConnections: 2},
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func TestNewFailsWithoutLeakingTheDatabaseURL(t *testing.T) {
	// Nothing listens on port 1, so the startup ping fails.
	cfg := testConfig("postgres://chat:s3cret@127.0.0.1:1/chat?sslmode=disable&connect_timeout=1")
	_, err := New(context.Background(), cfg, discardLogger())
	require.EqualError(t, err, "could not connect to PostgreSQL")
}

func TestNewRejectsAnUnparsableDatabaseURL(t *testing.T) {
	_, err := New(context.Background(), testConfig("postgres://chat:s3cret@[::1/chat"), discardLogger())
	require.EqualError(t, err, "invalid PostgreSQL connection configuration")
}

// The tests below need a reachable PostgreSQL (see compose.test.yaml); they
// skip without TEST_DATABASE_URL.

func TestReadyzReportsPostgres(t *testing.T) {
	application, err := New(context.Background(), testConfig(testdb.Migrated(t).URL), discardLogger())
	require.NoError(t, err)
	t.Cleanup(application.Close)

	server := httptest.NewServer(application.Handler())
	t.Cleanup(server.Close)
	response, err := http.Get(server.URL + "/readyz")
	require.NoError(t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.JSONEq(t, `{"status":"ready","dependencies":{"postgres":"ok"}}`, string(body))
}

// The service never migrates: without the migrate job it reports not ready.
func TestReadyzIsNotReadyWithoutMigrations(t *testing.T) {
	application, err := New(context.Background(), testConfig(testdb.Empty(t).URL), discardLogger())
	require.NoError(t, err)
	t.Cleanup(application.Close)

	recorder := httptest.NewRecorder()
	application.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	require.JSONEq(t, `{"status":"not_ready"}`, recorder.Body.String())
}

func TestRunServesUntilCancelled(t *testing.T) {
	cfg := testConfig(testdb.Migrated(t).URL)
	cfg.HTTP.Port = freePort(t)
	application, err := New(context.Background(), cfg, discardLogger())
	require.NoError(t, err)
	t.Cleanup(application.Close)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()

	url := "http://" + cfg.HTTP.Address() + "/readyz"
	require.Eventually(t, func() bool {
		response, err := http.Get(url)
		if err != nil {
			return false
		}
		response.Body.Close()
		return response.StatusCode == http.StatusOK
	}, 5*time.Second, 50*time.Millisecond)

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	_, port, err := net.SplitHostPort(listener.Addr().String())
	require.NoError(t, err)
	parsed, err := strconv.Atoi(port)
	require.NoError(t, err)
	return parsed
}
