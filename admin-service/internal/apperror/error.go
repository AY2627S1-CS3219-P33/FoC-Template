// Package apperror defines stable, transport-independent application errors.
package apperror

import "net/http"

type Code string

const (
	InvalidArgument Code = "INVALID_ARGUMENT"
	Unauthenticated Code = "UNAUTHENTICATED"
	Forbidden       Code = "FORBIDDEN"
	// DependencyUnavailable means a dependency (the identity provider or the
	// database) cannot answer.
	DependencyUnavailable Code = "DEPENDENCY_UNAVAILABLE"
	Internal              Code = "INTERNAL"
)

// Codes lists every code this service may return, in declaration order.
func Codes() []Code {
	return []Code{InvalidArgument, Unauthenticated, Forbidden, DependencyUnavailable, Internal}
}

// HTTPStatus is the single mapping from code to HTTP status. Unknown codes
// map to 500 so an unclassified failure never looks like a client error.
func HTTPStatus(code Code) int {
	switch code {
	case InvalidArgument:
		return http.StatusBadRequest
	case Unauthenticated:
		return http.StatusUnauthorized
	case Forbidden:
		return http.StatusForbidden
	case DependencyUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

type Field struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

type Error struct {
	Code    Code    `json:"code"`
	Message string  `json:"message"`
	Fields  []Field `json:"fields,omitempty"`
}

func (e *Error) Error() string { return e.Message }
