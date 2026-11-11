// AI Assistance Disclosure:
// Tool: ChatGPT (model: GPT-6 Luna), date: 2026-11-11
// Scope: Tests for user-service HTTP API handlers and readiness endpoints.
// Author review: I validated correctness, edited for style, and added boundary checks.

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"user-service/internal/repository"
)

func readyz(t *testing.T, check ReadinessCheck, method string) *httptest.ResponseRecorder {
	t.Helper()
	router := http.NewServeMux()
	RegisterReadiness(router, check)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(method, "/readyz", nil))
	return recorder
}

func TestReadyzReportsReady(t *testing.T) {
	recorder := readyz(t, func(context.Context) error { return nil }, http.MethodGet)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type = %q", got)
	}
	var body map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 1 || body["status"] != "ready" {
		t.Fatalf("body = %v, want only status ready", body)
	}
}

func TestReadyzReportsNotReadyWithoutLeakingTheCause(t *testing.T) {
	recorder := readyz(t, func(context.Context) error {
		return errors.New("dial postgres://user_service:secret@db:5432/users: connection refused")
	}, http.MethodGet)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
	if strings.TrimSpace(recorder.Body.String()) != `{"status":"not_ready"}` {
		t.Fatalf("body = %q", recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "secret") || strings.Contains(recorder.Body.String(), "postgres://") {
		t.Fatal("the failure's detail reached the response")
	}
}

func TestReadyzTurnsAHungCheckIntoNotReady(t *testing.T) {
	previous := readinessTimeout
	readinessTimeout = 50 * time.Millisecond
	t.Cleanup(func() { readinessTimeout = previous })
	started := time.Now()

	recorder := readyz(t, func(ctx context.Context) error {
		<-ctx.Done() // a database that never answers
		return ctx.Err()
	}, http.MethodGet)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("took %s; the check was not bounded", elapsed)
	}
}

func TestReadyzNeedsNoCredentialAndOnlyAnswersGet(t *testing.T) {
	ok := func(context.Context) error { return nil }

	if recorder := readyz(t, ok, http.MethodGet); recorder.Code != http.StatusOK {
		t.Fatalf("GET without a token = %d, want 200", recorder.Code)
	}
	if recorder := readyz(t, ok, http.MethodPost); recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST = %d, want 405", recorder.Code)
	}
}

func TestRegisterReadinessRequiresItsArguments(t *testing.T) {
	for name, call := range map[string]func(){
		"no router": func() { RegisterReadiness(nil, func(context.Context) error { return nil }) },
		"no check":  func() { RegisterReadiness(http.NewServeMux(), nil) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: expected a panic", name)
				}
			}()
			call()
		}()
	}
}

// With a real pool, /readyz follows the database: ready while it answers, not
// ready once the pool is closed (the nearest stand-in for a database that has
// gone away). It skips unless TEST_DATABASE_URL is set, like the repository
// tests.
func TestReadyzFollowsARealDatabase(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to run real PostgreSQL integration tests")
	}
	pool, err := repository.Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}

	if recorder := readyz(t, pool.Ping, http.MethodGet); recorder.Code != http.StatusOK {
		t.Fatalf("with the database up: status = %d, want 200", recorder.Code)
	}
	pool.Close()
	if recorder := readyz(t, pool.Ping, http.MethodGet); recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("with the pool closed: status = %d, want 503", recorder.Code)
	}
}
