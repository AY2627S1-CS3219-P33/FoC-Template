// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-10
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/auth"
)

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store") // chat content is private
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// writeError writes the standard error body, the same one the auth
// middleware writes.
func writeError(w http.ResponseWriter, code apperror.Code, message string) {
	auth.WriteError(w, code, message)
}

// writeChatError turns an error from chat.Service into a response. Only the
// service's own rule messages reach the client; anything on the server's side
// is logged and answered with a generic message.
func writeChatError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	code, message, serverFault := apperror.FromChat(err)
	if serverFault {
		logger.WarnContext(r.Context(), "chat request failed",
			"method", r.Method, "route", r.Pattern, "code", string(code), "error", err.Error())
	}
	writeError(w, code, message)
}
