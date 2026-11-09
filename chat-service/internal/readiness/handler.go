// Package readiness serves GET /readyz, unauthenticated, for the Compose
// health check and the ALB target group.
//
// Every registered check must pass for the service to be ready: chat-service
// cannot serve messages without PostgreSQL, and cannot open or close rooms
// without its RabbitMQ consumer (added with the event consumer). A ready
// response lists each dependency:
//
//	200 {"status":"ready","dependencies":{"postgres":"ok"}}
//	503 {"status":"not_ready"}
//
// A failing check's error is discarded, so error text, URLs and connection
// strings never reach the response. All checks run in parallel, each with its
// own deadline.
package readiness

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// DefaultTimeout bounds each check.
const DefaultTimeout = 2 * time.Second

// Dependency names, as they appear under "dependencies".
const (
	NamePostgres = "postgres"
)

// Check reports whether one dependency is usable. It must return soon after
// ctx is done.
type Check struct {
	Name  string
	Probe func(ctx context.Context) error
}

// Postgres checks the database, for example with (*pgxpool.Pool).Ping.
func Postgres(ping func(context.Context) error) Check {
	return Check{Name: NamePostgres, Probe: ping}
}

// Handler serves GET /readyz.
type Handler struct {
	checks  []Check
	timeout time.Duration
}

// NewHandler panics on a missing probe or a duplicate or empty name: those
// are wiring mistakes, not runtime conditions.
func NewHandler(checks ...Check) Handler {
	if len(checks) == 0 {
		panic("readiness: at least one check is required")
	}
	names := map[string]bool{}
	for _, check := range checks {
		if check.Name == "" {
			panic("readiness: check name is required")
		}
		if check.Probe == nil {
			panic("readiness: probe is required for " + check.Name)
		}
		if names[check.Name] {
			panic("readiness: duplicate check " + check.Name)
		}
		names[check.Name] = true
	}
	return Handler{checks: checks, timeout: DefaultTimeout}
}

// WithTimeout returns h with each check bounded by timeout instead of
// DefaultTimeout.
func (h Handler) WithTimeout(timeout time.Duration) Handler {
	if timeout <= 0 {
		panic("readiness: timeout must be positive")
	}
	h.timeout = timeout
	return h
}

func (h Handler) RegisterRoutes(router *http.ServeMux) {
	router.HandleFunc("GET /readyz", h.get)
}

type report struct {
	Status       string            `json:"status"`
	Dependencies map[string]string `json:"dependencies,omitempty"`
}

func (h Handler) get(response http.ResponseWriter, request *http.Request) {
	ok := h.runAll(request.Context())

	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	body := report{Status: "ready", Dependencies: make(map[string]string, len(h.checks))}
	for i, check := range h.checks {
		if !ok[i] {
			response.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(response).Encode(report{Status: "not_ready"})
			return
		}
		body.Dependencies[check.Name] = "ok"
	}
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(body)
}

// runAll runs every check in parallel, each under its own deadline, and
// reports which passed, in the order of h.checks. A probe that ignores its
// context counts as failed at the deadline and finishes in the background.
func (h Handler) runAll(ctx context.Context) []bool {
	ok := make([]bool, len(h.checks))
	var wg sync.WaitGroup
	for i, check := range h.checks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			checkCtx, cancel := context.WithTimeout(ctx, h.timeout)
			defer cancel()
			result := make(chan error, 1)
			go func() { result <- check.Probe(checkCtx) }()
			select {
			case err := <-result:
				ok[i] = err == nil
			case <-checkCtx.Done():
			}
		}()
	}
	wg.Wait()
	return ok
}
