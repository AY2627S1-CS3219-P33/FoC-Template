// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-10
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

// Package apperror defines the stable JSON error body every chat-service
// route returns, in the same shape as order-, credit- and supplier-service:
//
//	{"code": "ROOM_NOT_FOUND", "message": "room not found"}
//
// Clients branch on code, never on message: codes are a contract, messages
// are for people and may change.
package apperror

import "net/http"

type Code string

const (
	// InvalidArgument: a path or query parameter is malformed (e.g. a room ID
	// that is not a UUID, or limit out of range).
	InvalidArgument Code = "INVALID_ARGUMENT"
	// InvalidMessage: the message body or clientMsgId breaks a rule.
	InvalidMessage  Code = "INVALID_MESSAGE"
	Unauthenticated Code = "UNAUTHENTICATED"
	Forbidden       Code = "FORBIDDEN"
	// RoomNotFound: no such room, or the caller is not a participant. The two
	// look the same on purpose, so outsiders cannot tell a room exists.
	RoomNotFound Code = "ROOM_NOT_FOUND"
	// RoomClosed: the room is read-only; sending is refused.
	RoomClosed Code = "ROOM_CLOSED"
	// RateLimited: too many messages in a short time (WebSocket, phase 5).
	RateLimited           Code = "RATE_LIMITED"
	DependencyUnavailable Code = "DEPENDENCY_UNAVAILABLE"
	Internal              Code = "INTERNAL"
)

// Codes lists every code this service may return, in declaration order.
func Codes() []Code {
	return []Code{
		InvalidArgument,
		InvalidMessage,
		Unauthenticated,
		Forbidden,
		RoomNotFound,
		RoomClosed,
		RateLimited,
		DependencyUnavailable,
		Internal,
	}
}

// HTTPStatus is the single mapping from code to HTTP status. Unknown codes
// map to 500 so an unclassified failure never looks like a client error.
func HTTPStatus(code Code) int {
	switch code {
	case InvalidArgument, InvalidMessage:
		return http.StatusBadRequest
	case Unauthenticated:
		return http.StatusUnauthorized
	case Forbidden:
		return http.StatusForbidden
	case RoomNotFound:
		return http.StatusNotFound
	case RoomClosed:
		return http.StatusConflict
	case RateLimited:
		return http.StatusTooManyRequests
	case DependencyUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

type Error struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }
