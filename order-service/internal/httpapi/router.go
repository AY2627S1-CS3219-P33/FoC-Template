// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-10-18
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

package httpapi

import (
	"log/slog"
	"net/http"
)

type Dependencies struct {
	Logger *slog.Logger
}

// RouteRegistrar lets each feature own its paths. Adding a feature only changes
// application composition; it never requires editing the central router.
type RouteRegistrar interface {
	RegisterRoutes(*http.ServeMux)
}

func NewRouter(dependencies Dependencies, registrars ...RouteRegistrar) http.Handler {
	if dependencies.Logger == nil {
		panic("httpapi: logger is required")
	}

	router := http.NewServeMux()
	for _, registrar := range registrars {
		if registrar == nil {
			panic("httpapi: route registrar is required")
		}
		registrar.RegisterRoutes(router)
	}
	return router
}
