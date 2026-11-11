// AI Assistance Disclosure:
// Tool: ChatGPT (model: GPT-6 Luna), date: 2026-11-11
// Scope: Implementation of user-service HTTP API handlers and readiness endpoints.
// Author review: I validated correctness, edited for style, and added boundary checks.

package handler

import (
	"context"
	"net/http"
	"time"
)

// readinessTimeout bounds one readiness check, so a hung database turns into
// "not ready" instead of a hung probe.
var readinessTimeout = 2 * time.Second

// ReadinessCheck reports whether the service can do its work. A nil error means
// ready. The error is never shown to the caller: it can hold connection
// details.
type ReadinessCheck func(context.Context) error

// RegisterReadiness serves GET /readyz, unauthenticated, in the same shape as
// supplier-service and credit-service: 200 {"status":"ready"}, or
// 503 {"status":"not_ready"}. Unlike GET /health, which only says the process
// answers, it says whether the service's database does too, so the admin
// dashboard can show a real answer for user-service.
func RegisterReadiness(router *http.ServeMux, check ReadinessCheck) {
	if router == nil || check == nil {
		panic("handler: router and readiness check are required")
	}
	router.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
		defer cancel()
		if err := check(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
}
