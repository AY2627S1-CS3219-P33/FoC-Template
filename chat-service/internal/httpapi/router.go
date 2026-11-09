// Package httpapi mounts chat-service's REST routes. The participant and
// admin routes arrive in later phases; for now it serves only what the
// registrars passed to NewRouter provide (GET /readyz).
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
