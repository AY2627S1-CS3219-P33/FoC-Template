// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: Implementation of notification-service API error handling.
// Author review: I validated correctness, edited for style, and added boundary checks.

// Package apperror defines the stable JSON error body returned by the API.
package apperror

type Code string

const (
	InvalidArgument       Code = "INVALID_ARGUMENT"
	Unauthenticated       Code = "UNAUTHENTICATED"
	Forbidden             Code = "FORBIDDEN"
	NotificationNotFound  Code = "NOTIFICATION_NOT_FOUND"
	DependencyUnavailable Code = "DEPENDENCY_UNAVAILABLE"
	Internal              Code = "INTERNAL"
)

type Error struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }
