// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-10
// Scope: Implementation of test code (unit and integration tests).
// Author review: Reviewed, validated, and tested by the authors.

package readiness

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// leakyError carries the kind of detail the response must never show.
var leakyError = errors.New("dial tcp: postgres://chat:s3cret@db.internal:5432/chat refused")

func probe(up bool) func(context.Context) error {
	return func(context.Context) error {
		if up {
			return nil
		}
		return leakyError
	}
}

func serve(t *testing.T, handler Handler) *httptest.ResponseRecorder {
	t.Helper()
	router := http.NewServeMux()
	handler.RegisterRoutes(router)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	require.Equal(t, "application/json", recorder.Header().Get("Content-Type"))
	require.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
	return recorder
}

func TestReadyWhenEveryCheckPasses(t *testing.T) {
	recorder := serve(t, NewHandler(Postgres(probe(true)), Check{Name: "rabbitmq", Probe: probe(true)}))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.JSONEq(t, `{"status":"ready","dependencies":{"postgres":"ok","rabbitmq":"ok"}}`, recorder.Body.String())
}

func TestNotReadyWhenAnyCheckFails(t *testing.T) {
	for _, tt := range []struct {
		name             string
		postgres, rabbit bool
	}{
		{"postgres down", false, true},
		{"rabbitmq down", true, false},
		{"both down", false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			recorder := serve(t, NewHandler(Postgres(probe(tt.postgres)), Check{Name: "rabbitmq", Probe: probe(tt.rabbit)}))
			require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
			require.JSONEq(t, `{"status":"not_ready"}`, recorder.Body.String())
			require.NotContains(t, recorder.Body.String(), "s3cret")
		})
	}
}

func TestOptionalCheckDegradesButStaysReady(t *testing.T) {
	running := false
	handler := NewHandler(Postgres(probe(true)), ErrandEvents(func() bool { return running }))

	recorder := serve(t, handler)
	require.Equal(t, http.StatusOK, recorder.Code, "the consumer being down does not take chat offline")
	require.JSONEq(t, `{"status":"degraded","dependencies":{"postgres":"ok","errandEvents":"down"}}`, recorder.Body.String())

	running = true
	recorder = serve(t, handler)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.JSONEq(t, `{"status":"ready","dependencies":{"postgres":"ok","errandEvents":"ok"}}`, recorder.Body.String())

	recorder = serve(t, NewHandler(Postgres(probe(false)), ErrandEvents(func() bool { return true })))
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code, "a required check still decides")
	require.JSONEq(t, `{"status":"not_ready"}`, recorder.Body.String())
}

func TestHungCheckTimesOut(t *testing.T) {
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })
	ignoresContext := func(context.Context) error { <-hang; return nil }

	start := time.Now()
	recorder := serve(t, NewHandler(Postgres(ignoresContext)).WithTimeout(50*time.Millisecond))
	require.Less(t, time.Since(start), 2*time.Second, "a hung probe does not hang /readyz")
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
}

func TestChecksRunInParallelWithTheirOwnDeadline(t *testing.T) {
	// Each probe waits for the other, which only works if they run at once.
	first, second := make(chan struct{}), make(chan struct{})
	waitFor := func(mine, other chan struct{}) func(context.Context) error {
		return func(ctx context.Context) error {
			if _, ok := ctx.Deadline(); !ok {
				return errors.New("no deadline")
			}
			close(mine)
			select {
			case <-other:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	recorder := serve(t, NewHandler(
		Postgres(waitFor(first, second)),
		Check{Name: "other", Probe: waitFor(second, first)},
	).WithTimeout(5*time.Second))
	require.Equal(t, http.StatusOK, recorder.Code)
}

func TestNewHandlerRejectsWiringMistakes(t *testing.T) {
	require.Panics(t, func() { NewHandler() })
	require.Panics(t, func() { NewHandler(Check{Name: "", Probe: probe(true)}) })
	require.Panics(t, func() { NewHandler(Postgres(nil)) })
	require.Panics(t, func() { NewHandler(Postgres(probe(true)), Postgres(probe(true))) })
	require.Panics(t, func() { NewHandler(Postgres(probe(true))).WithTimeout(0) })
	require.Panics(t, func() { ErrandEvents(nil) })
}
