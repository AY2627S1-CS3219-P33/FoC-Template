// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-10-18
// Scope: Implementation of test code (unit and integration tests).
// Author review: Reviewed, validated, and tested by the authors.

package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

type testRegistrar struct{}

func (testRegistrar) RegisterRoutes(router *http.ServeMux) {
	router.HandleFunc("GET /feature-route", func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	})
}

func TestRouterComposesFeatureRegistrars(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	router := NewRouter(Dependencies{Logger: logger}, testRegistrar{})
	request := httptest.NewRequest(http.MethodGet, "/feature-route", nil)
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusNoContent, response.Code)
}
