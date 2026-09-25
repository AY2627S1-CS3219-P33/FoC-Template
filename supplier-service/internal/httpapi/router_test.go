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
