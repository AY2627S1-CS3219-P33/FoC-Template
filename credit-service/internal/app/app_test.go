package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

type checkerStub struct{ err error }

func (c checkerStub) Check(context.Context) error { return c.err }

func TestHandlerServesReadyzWithoutOpeningAPort(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	response := httptest.NewRecorder()
	Handler(checkerStub{}).ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code)

	response = httptest.NewRecorder()
	Handler(checkerStub{err: errors.New("down")}).ServeHTTP(response, request)
	require.Equal(t, http.StatusServiceUnavailable, response.Code)
}
