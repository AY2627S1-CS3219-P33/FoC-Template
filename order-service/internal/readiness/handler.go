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

// Handler serves GET /readyz.
type Handler struct {
	postgres Checker
	checkers []Checker
	timeout  time.Duration
}

// NewHandler reports postgres, which decides readiness, and checkers, which
// are reported individually. Names must be unique and non-empty.
func NewHandler(postgres Checker, checkers ...Checker) Handler {
	if postgres == nil {
		panic("readiness: postgres checker is required")
	}
	names := map[string]bool{}
	for _, checker := range append([]Checker{postgres}, checkers...) {
		if checker == nil {
			panic("readiness: checker is required")
		}
		name := checker.Name()
		if name == "" {
			panic("readiness: checker name is required")
		}
		if names[name] {
			panic("readiness: duplicate checker " + name)
		}
		names[name] = true
	}
	return Handler{postgres: postgres, checkers: checkers, timeout: DefaultTimeout}
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
	Dependencies map[string]Status `json:"dependencies,omitempty"`
}

func (h Handler) get(response http.ResponseWriter, request *http.Request) {
	all := append([]Checker{h.postgres}, h.checkers...)
	statuses := h.checkAll(request.Context(), all)

	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	if statuses[0] != StatusOK {
		response.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(response).Encode(report{Status: "not_ready"})
		return
	}

	body := report{Status: "ready", Dependencies: make(map[string]Status, len(all))}
	for i, checker := range all {
		status := statuses[i]
		if !status.reportable() {
			status = StatusDegraded
		}
		if !status.healthy() {
			body.Status = "degraded"
		}
		body.Dependencies[checker.Name()] = status
	}
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(body)
}

// checkAll runs every check in parallel, each under its own deadline, and
// returns the statuses in the order of checkers.
func (h Handler) checkAll(ctx context.Context, checkers []Checker) []Status {
	statuses := make([]Status, len(checkers))
	var wg sync.WaitGroup
	for i, checker := range checkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			checkCtx, cancel := context.WithTimeout(ctx, h.timeout)
			defer cancel()
			statuses[i] = checker.Check(checkCtx)
		}()
	}
	wg.Wait()
	return statuses
}
