// Package readiness exposes the service readiness route.
package readiness

import (
	"context"
	"encoding/json"
	"net/http"
)

type Checker interface {
	Ping(context.Context) error
}

type Handler struct {
	checker Checker
}

func NewHandler(checker Checker) Handler {
	if checker == nil {
		panic("readiness: checker is required")
	}
	return Handler{checker: checker}
}

func (h Handler) RegisterRoutes(router *http.ServeMux) {
	router.HandleFunc("GET /readyz", h.get)
}

func (h Handler) get(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-Type", "application/json")
	if err := h.checker.Ping(request.Context()); err != nil {
		response.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(response).Encode(map[string]string{"status": "not_ready"})
		return
	}
	_ = json.NewEncoder(response).Encode(map[string]string{"status": "ready"})
}
