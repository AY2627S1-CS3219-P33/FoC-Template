// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-10
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

package apperror

import (
	"errors"
	"strings"

	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/chat"
)

// FromChat turns an error from package chat into a code and a message that
// is safe to show the client. serverFault is true when the error is on the
// server's side (log it); then message is generic and never the error text,
// which can contain database details. REST and WebSocket both use it, so a
// client sees the same codes on either.
func FromChat(err error) (code Code, message string, serverFault bool) {
	switch {
	case errors.Is(err, chat.ErrRoomNotFound):
		return RoomNotFound, "room not found", false
	case errors.Is(err, chat.ErrRoomClosed):
		return RoomClosed, "this chat is closed; messages can no longer be sent", false
	case errors.Is(err, chat.ErrInvalidMessage):
		return InvalidMessage, ruleMessage(err, chat.ErrInvalidMessage, "message is invalid"), false
	case errors.Is(err, chat.ErrInvalidPage):
		return InvalidArgument, ruleMessage(err, chat.ErrInvalidPage, "paging parameters are invalid"), false
	case errors.Is(err, chat.ErrUnavailable):
		return DependencyUnavailable, "chat storage is temporarily unavailable", true
	default:
		return Internal, "an unexpected internal error occurred", true
	}
}

// ruleMessage returns the explanation chat attached to a rule error ("invalid
// message: body must be 1 to 2000 characters" gives "body must be 1 to 2000
// characters"), or fallback when there is none, for example when the
// database rejected the value instead.
func ruleMessage(err, sentinel error, fallback string) string {
	text := err.Error()
	prefix := sentinel.Error() + ": "
	if strings.HasPrefix(text, prefix) {
		return strings.TrimPrefix(text, prefix)
	}
	return fallback
}
