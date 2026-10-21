// Package errandhttp holds the HTTP pieces Person A's errand handlers share
// (A2–A7): the caller's identity, error responses, and the response schemas
// of openapi/common.yaml. It makes no decisions about errands; those are
// errand.Next's.
package errandhttp

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/auth"
)

// Caller returns the caller's sub from the principal auth.Middleware stored,
// writing 401 when there is none. Identity never comes from request data
// (NFR3.3.1).
func Caller(response http.ResponseWriter, request *http.Request) (string, bool) {
	principal, found := auth.PrincipalFromContext(request.Context())
	if !found || principal.Subject == "" {
		WriteError(response, apperror.Unauthenticated, "authentication is required")
		return "", false
	}
	return principal.Subject, true
}

func WriteJSON(response http.ResponseWriter, status int, body any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(body)
}

// WriteError writes the standard error body with apperror.HTTPStatus.
func WriteError(response http.ResponseWriter, code apperror.Code, message string) {
	auth.WriteError(response, code, message)
}

// WriteValidation writes 400 VALIDATION_FAILED naming every invalid field
// (NFR2.2.1).
func WriteValidation(response http.ResponseWriter, fields ...apperror.Field) {
	WriteJSON(response, apperror.HTTPStatus(apperror.ValidationFailed), apperror.Error{
		Code:    apperror.ValidationFailed,
		Message: "request body is invalid",
		Fields:  fields,
	})
}

// Fail writes err. An *apperror.Error is the client's answer and is written
// as is; anything else is logged and hidden behind 500 INTERNAL. Only the
// operation and errand ID are logged, never request fields, so the contact
// person never reaches the logs.
func Fail(response http.ResponseWriter, request *http.Request, logger *slog.Logger, operation, errandID string, err error) {
	var appErr *apperror.Error
	if errors.As(err, &appErr) {
		if appErr.Code == apperror.ValidationFailed {
			WriteValidation(response, appErr.Fields...)
			return
		}
		WriteError(response, appErr.Code, appErr.Message)
		return
	}
	attributes := []any{slog.String("operation", operation), slog.Any("error", err)}
	if errandID != "" {
		attributes = append(attributes, slog.String("errand_id", errandID))
	}
	logger.ErrorContext(request.Context(), "errand request failed", attributes...)
	WriteError(response, apperror.Internal, "an unexpected internal error occurred")
}
