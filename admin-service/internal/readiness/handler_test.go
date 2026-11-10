package readiness_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/readiness"
)

func ready(t *testing.T, handler readiness.Handler) (int, map[string]any) {
	t.Helper()
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	var body map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	return recorder.Code, body
}

func TestReadyWhenEverythingIsUp(t *testing.T) {
	code, body := ready(t, readiness.NewHandler(
		readiness.Postgres(func(context.Context) error { return nil }),
		readiness.Events(func() bool { return true }),
	))

	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "ready", body["status"])
	require.Equal(t, map[string]any{"postgres": "ok", "events": "running"}, body["dependencies"])
}

func TestStoppedConsumerDegradesButStaysServing(t *testing.T) {
	code, body := ready(t, readiness.NewHandler(
		readiness.Postgres(func(context.Context) error { return nil }),
		readiness.Events(func() bool { return false }),
	))

	require.Equal(t, http.StatusOK, code, "stored facts can still be served")
	require.Equal(t, "degraded", body["status"])
	require.Equal(t, "stopped", body["dependencies"].(map[string]any)["events"])
}

func TestPostgresDownIsNotReadyAndLeaksNothing(t *testing.T) {
	code, body := ready(t, readiness.NewHandler(
		readiness.Postgres(func(context.Context) error { return errors.New("postgres://admin:secret@db:5432 refused") }),
		readiness.Events(func() bool { return true }),
	))

	require.Equal(t, http.StatusServiceUnavailable, code)
	require.Equal(t, map[string]any{"status": "not_ready"}, body)
}

func TestSlowPostgresTimesOut(t *testing.T) {
	handler := readiness.NewHandler(
		readiness.Postgres(func(ctx context.Context) error { time.Sleep(time.Second); return nil }),
	).WithTimeout(20 * time.Millisecond)

	code, _ := ready(t, handler)

	require.Equal(t, http.StatusServiceUnavailable, code)
}

func TestUnknownStatusIsShownAsDegraded(t *testing.T) {
	code, body := ready(t, readiness.NewHandler(
		readiness.Postgres(func(context.Context) error { return nil }),
		readiness.Func("odd", func(context.Context) readiness.Status { return "dial tcp 10.0.0.1: refused" }),
	))

	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "degraded", body["dependencies"].(map[string]any)["odd"])
}
