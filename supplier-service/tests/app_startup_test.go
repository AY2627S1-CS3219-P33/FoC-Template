package tests

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/app"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/config"
)

func TestApplicationStarts(t *testing.T) {
	cfg := config.Config{
		Environment: config.Test,
		HTTP: config.HTTPConfig{
			Host: "127.0.0.1",
			Port: 3002,
		},
		Database: config.DatabaseConfig{
			URL:            "postgresql://supplier_test@127.0.0.1:5433/supplier_test",
			MinConnections: 0,
			MaxConnections: 2,
		},
		LogLevel:        "error",
		ShutdownTimeout: time.Second,
	}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	application, err := app.New(context.Background(), cfg, logger)
	require.NoError(t, err)
	t.Cleanup(application.Close)

	request := httptest.NewRequest(http.MethodGet, "/not-implemented", nil)
	response := httptest.NewRecorder()
	application.Handler().ServeHTTP(response, request)

	require.Equal(t, http.StatusNotFound, response.Code)
}
