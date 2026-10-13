package apperror

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHTTPStatusMapsEveryCode(t *testing.T) {
	expected := map[Code]int{
		InvalidArgument:            http.StatusBadRequest,
		ValidationFailed:           http.StatusBadRequest,
		Unauthenticated:            http.StatusUnauthorized,
		Forbidden:                  http.StatusForbidden,
		NotEligible:                http.StatusForbidden,
		CourierRestricted:          http.StatusForbidden,
		ErrandNotFound:             http.StatusNotFound,
		ErrandChanged:              http.StatusConflict,
		ErrandUnavailable:          http.StatusConflict,
		InvalidTransition:          http.StatusConflict,
		InsufficientCredits:        http.StatusConflict,
		SupplierDeletionInProgress: http.StatusConflict,
		SupplierUnavailable:        http.StatusUnprocessableEntity,
		DependencyUnavailable:      http.StatusServiceUnavailable,
		Internal:                   http.StatusInternalServerError,
	}
	require.Len(t, Codes(), len(expected), "every code needs an expected status")
	for _, code := range Codes() {
		status, ok := expected[code]
		require.True(t, ok, "missing expectation for %s", code)
		require.Equal(t, status, HTTPStatus(code), string(code))
	}
}

func TestHTTPStatusUnknownCodeIsInternal(t *testing.T) {
	require.Equal(t, http.StatusInternalServerError, HTTPStatus(""))
	require.Equal(t, http.StatusInternalServerError, HTTPStatus("SOMETHING_NEW"))
}

func TestCodesAreUnique(t *testing.T) {
	seen := map[Code]bool{}
	for _, code := range Codes() {
		require.False(t, seen[code], "duplicate code %s", code)
		seen[code] = true
	}
}

func TestErrorJSONShape_NFR2_2_1(t *testing.T) {
	err := &Error{
		Code:    ValidationFailed,
		Message: "errand is invalid",
		Fields:  []Field{{Field: "reward", Message: "must be a positive integer"}},
	}
	body, marshalErr := json.Marshal(err)
	require.NoError(t, marshalErr)
	require.JSONEq(t, `{"code":"VALIDATION_FAILED","message":"errand is invalid","fields":[{"field":"reward","message":"must be a positive integer"}]}`, string(body))
	require.Equal(t, "errand is invalid", err.Error())

	body, marshalErr = json.Marshal(&Error{Code: Forbidden, Message: "no"})
	require.NoError(t, marshalErr)
	require.JSONEq(t, `{"code":"FORBIDDEN","message":"no"}`, string(body))
}
