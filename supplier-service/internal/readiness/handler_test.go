package readiness

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

type checkerStub struct{ err error }

func (c checkerStub) Ping(context.Context) error { return c.err }

func TestReadinessReportsReady(t *testing.T) {
	response := serveReadiness(checkerStub{})
	require.Equal(t, http.StatusOK, response.Code)
	require.JSONEq(t, `{"status":"ready"}`, response.Body.String())
}

func TestReadinessFailsClosedWithoutLeakingDependencyError(t *testing.T) {
	response := serveReadiness(checkerStub{err: errors.New("database secret")})
	require.Equal(t, http.StatusServiceUnavailable, response.Code)
	require.JSONEq(t, `{"status":"not_ready"}`, response.Body.String())
	require.NotContains(t, response.Body.String(), "database secret")
}

func serveReadiness(checker Checker) *httptest.ResponseRecorder {
	router := http.NewServeMux()
	NewHandler(checker).RegisterRoutes(router)
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}
