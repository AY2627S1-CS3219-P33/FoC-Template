// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-10
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/chat"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/protocol"
)

// maxBodyBytes bounds a request body: a 2000-character message is at most
// 8000 bytes of UTF-8, plus JSON.
const maxBodyBytes = 16 * 1024

// ChatHandler serves the participant routes. Every route needs a user token
// with chats:use; the caller's identity is taken only from that token.
type ChatHandler struct {
	service *chat.Service
	guard   *auth.Middleware
	logger  *slog.Logger
}

func NewChatHandler(service *chat.Service, guard *auth.Middleware, logger *slog.Logger) *ChatHandler {
	if service == nil || guard == nil || logger == nil {
		panic("httpapi: service, guard and logger are required")
	}
	return &ChatHandler{service: service, guard: guard, logger: logger}
}

func (h *ChatHandler) RegisterRoutes(router *http.ServeMux) {
	use := func(handler http.HandlerFunc) http.Handler { return h.guard.RequireUse(handler) }
	router.Handle("GET /me/chats", use(h.listMine))
	router.Handle("GET /errands/{errandId}/chats", use(h.listForErrand))
	router.Handle("GET /chats/{roomId}/messages", use(h.listMessages))
	router.Handle("POST /chats/{roomId}/messages", use(h.sendMessage))
}

// ---- Response bodies (see openapi.yaml; shapes in package protocol) ----

type listJSON[T any] struct {
	Items []T `json:"items"`
}

func roomsResponse(views []chat.RoomView) listJSON[protocol.Room] {
	items := make([]protocol.Room, len(views))
	for i, view := range views {
		items[i] = protocol.FromRoomView(view)
	}
	return listJSON[protocol.Room]{Items: items}
}

// ---- Handlers ----

// listMine serves GET /me/chats: every room of the caller, newest first.
func (h *ChatHandler) listMine(w http.ResponseWriter, r *http.Request) {
	principal, _ := auth.PrincipalFromContext(r.Context())
	views, err := h.service.MyRooms(r.Context(), principal.Subject)
	if err != nil {
		writeChatError(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, roomsResponse(views))
}

// listForErrand serves GET /errands/{errandId}/chats: the caller's rooms
// for one errand. An empty list means no room exists yet for the caller.
func (h *ChatHandler) listForErrand(w http.ResponseWriter, r *http.Request) {
	errandID, ok := pathUUID(w, r, "errandId")
	if !ok {
		return
	}
	principal, _ := auth.PrincipalFromContext(r.Context())
	views, err := h.service.RoomsForErrand(r.Context(), principal.Subject, errandID)
	if err != nil {
		writeChatError(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, roomsResponse(views))
}

// listMessages serves GET /chats/{roomId}/messages?after=|before=&limit=.
func (h *ChatHandler) listMessages(w http.ResponseWriter, r *http.Request) {
	roomID, ok := pathUUID(w, r, "roomId")
	if !ok {
		return
	}
	page, ok := pageFromQuery(w, r)
	if !ok {
		return
	}
	principal, _ := auth.PrincipalFromContext(r.Context())
	messages, err := h.service.History(r.Context(), principal.Subject, roomID, page)
	if err != nil {
		writeChatError(w, r, h.logger, err)
		return
	}
	items := make([]protocol.Message, len(messages))
	for i, message := range messages {
		items[i] = protocol.FromMessageView(message)
	}
	writeJSON(w, http.StatusOK, listJSON[protocol.Message]{Items: items})
}

type sendRequest struct {
	ClientMsgID string `json:"clientMsgId"`
	Body        string `json:"body"`
}

// sendMessage serves POST /chats/{roomId}/messages: 201 with the new
// message, or 200 with the stored one when this is a retry of the same
// clientMsgId.
func (h *ChatHandler) sendMessage(w http.ResponseWriter, r *http.Request) {
	roomID, ok := pathUUID(w, r, "roomId")
	if !ok {
		return
	}
	var request sendRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	principal, _ := auth.PrincipalFromContext(r.Context())
	message, created, err := h.service.Send(r.Context(), principal.Subject, roomID, request.ClientMsgID, request.Body)
	if err != nil {
		writeChatError(w, r, h.logger, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, protocol.FromMessageView(message))
}

// ---- Request parsing ----

func pathUUID(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil {
		writeError(w, apperror.InvalidArgument, name+" must be a UUID")
		return uuid.UUID{}, false
	}
	return id, true
}

// pageFromQuery reads after, before and limit. chat.Service checks the
// combinations and ranges.
func pageFromQuery(w http.ResponseWriter, r *http.Request) (chat.Page, bool) {
	query := r.URL.Query()
	var page chat.Page
	for _, param := range []struct {
		name string
		dest **int64
	}{{"after", &page.After}, {"before", &page.Before}} {
		if !query.Has(param.name) {
			continue
		}
		value, err := strconv.ParseInt(query.Get(param.name), 10, 64)
		if err != nil {
			writeError(w, apperror.InvalidArgument, param.name+" must be a message ID")
			return chat.Page{}, false
		}
		*param.dest = &value
	}
	if query.Has("limit") {
		limit, err := strconv.Atoi(query.Get("limit"))
		if err != nil || limit < 1 {
			writeError(w, apperror.InvalidArgument, "limit must be between 1 and "+strconv.Itoa(chat.MaxPageSize))
			return chat.Page{}, false
		}
		page.Limit = limit
	}
	return page, true
}

// decodeJSON reads one JSON object with no unknown fields and nothing after
// it, at most maxBodyBytes long.
func decodeJSON(w http.ResponseWriter, r *http.Request, dest any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dest); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, apperror.InvalidArgument, "request body is too large")
			return false
		}
		writeError(w, apperror.InvalidArgument, "request body must be a JSON object with clientMsgId and body")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, apperror.InvalidArgument, "request body must contain one JSON object")
		return false
	}
	return true
}
