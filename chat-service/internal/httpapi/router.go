// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-10
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

// Package httpapi mounts chat-service's REST routes: whatever the registrars
// passed to NewRouter provide (the participant chat routes, GET /ws and
// GET /readyz).
package httpapi

import "net/http"

// RouteRegistrar lets each component own its paths, so adding one changes
// only the composition in package app.
type RouteRegistrar interface {
	RegisterRoutes(*http.ServeMux)
}

func NewRouter(registrars ...RouteRegistrar) http.Handler {
	router := http.NewServeMux()
	for _, registrar := range registrars {
		if registrar == nil {
			panic("httpapi: route registrar is required")
		}
		registrar.RegisterRoutes(router)
	}
	return router
}
