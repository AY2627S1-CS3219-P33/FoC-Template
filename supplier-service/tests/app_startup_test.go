package tests

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/app"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/config"
)

func startupConfig() config.Config {
	authConfig := auth.DefaultAuth0Config()
	authConfig.Issuer = "https://tenant.example.com/"
	return config.Config{
		Auth: authConfig, Environment: config.Test,
		HTTP:        config.HTTPConfig{Host: "127.0.0.1", Port: 3002},
		Database:    config.DatabaseConfig{URL: "postgresql://supplier_test@127.0.0.1:5433/supplier_test", MaxConnections: 2},
		SeedCSVPath: "../../data/csv/supplier-seed-data.csv", SeedNamespace: "template-v1",
		StartupTimeout: 10 * time.Second, ShutdownTimeout: time.Second,
	}
}

func TestStartupRejectsInvalidSeedConfiguration_NFR4_1_4(t *testing.T) {
	for _, missingPath := range []bool{true, false} {
		cfg := startupConfig()
		if missingPath {
			cfg.SeedCSVPath = ""
		} else {
			cfg.SeedCSVPath = filepath.Join(t.TempDir(), "secret-private-path.csv")
		}
		application, err := app.New(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
		require.Error(t, err)
		require.Nil(t, application)
		require.NotContains(t, err.Error(), "secret-private-path")
	}
}

func TestApplicationStartupSeedsIdempotently_NFR4_1_4(t *testing.T) {
	databaseURL, conn := startupDatabase(t)
	cfg := startupConfig()
	cfg.Database.URL = databaseURL
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	application, err := app.New(ctx, cfg, logger)
	require.NoError(t, err)
	t.Cleanup(application.Close)
	var count int
	require.NoError(t, conn.QueryRow(ctx, "SELECT count(*) FROM supplier_seed_provenance").Scan(&count))
	require.Positive(t, count)
	response := httptest.NewRecorder()
	application.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/suppliers", nil))
	require.Equal(t, http.StatusUnauthorized, response.Code)
	application.Close()

	application, err = app.New(ctx, cfg, logger)
	require.NoError(t, err)
	t.Cleanup(application.Close)
	var repeated int
	require.NoError(t, conn.QueryRow(ctx, "SELECT count(*) FROM supplier_seed_provenance").Scan(&repeated))
	require.Equal(t, count, repeated)
	require.NoError(t, conn.QueryRow(ctx, "SELECT count(*) FROM supplier_versions").Scan(&repeated))
	require.Equal(t, count, repeated)
}

func TestStartupRejectsMissingSchema_NFR4_1_4(t *testing.T) {
	databaseURL, conn := startupDatabase(t)
	_, err := conn.Exec(context.Background(), "DROP TABLE supplier_seed_provenance")
	require.NoError(t, err)
	cfg := startupConfig()
	cfg.Database.URL = databaseURL
	application, err := app.New(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.Error(t, err)
	require.Nil(t, application)
	require.Contains(t, err.Error(), "seed rows could not be stored")
	require.NotContains(t, err.Error(), databaseURL)
}

// Each integration test owns a disposable schema; existing database data is untouched.
func startupDatabase(t *testing.T) (string, *pgx.Conn) {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set; skipping application PostgreSQL integration test")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, databaseURL)
	require.NoError(t, err)
	schema := fmt.Sprintf("supplier_app_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	_, err = conn.Exec(ctx, "CREATE SCHEMA "+quoted)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE")
		_ = conn.Close(context.Background())
	})
	_, err = conn.Exec(ctx, "SET search_path TO "+quoted)
	require.NoError(t, err)
	migration, err := os.ReadFile("../migrations/00001_create_versioned_suppliers.sql")
	require.NoError(t, err)
	up := strings.Split(strings.Split(string(migration), "-- +goose Up")[1], "-- +goose Down")[0]
	_, err = conn.Exec(ctx, up)
	require.NoError(t, err)
	u, err := url.Parse(databaseURL)
	require.NoError(t, err)
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	return u.String(), conn
}
