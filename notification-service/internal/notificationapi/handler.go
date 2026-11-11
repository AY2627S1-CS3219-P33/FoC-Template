// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: Implementation of notification-service notification HTTP API.
// Author review: I validated correctness, edited for style, and added boundary checks.

// Package notificationapi exposes the notification use cases over HTTP.
//
//	GET  /v1/notifications               user  notifications:read:own
//	GET  /v1/notifications/unread-count   user  notifications:read:own
//	GET  /v1/notifications/stream         user  notifications:read:own
//	POST /v1/notifications/{id}/read      user  notifications:write:own
//	POST /v1/notifications/read-all       user  notifications:write:own
//
// A user only ever sees their own notifications: every route resolves the
// recipient from the verified token and takes no user identifier from the
// request. The stream route pushes the unread count over Server-Sent Events so
// the client does not poll.
package notificationapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/notification-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/notification-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/notification-service/internal/notification"
)

// Service is the use-case boundary the handlers depend on.
type Service interface {
	List(ctx context.Context, recipient string, opts notification.ListOptions) ([]notification.Notification, string, error)
	UnreadCount(ctx context.Context, recipient string) (int64, error)
	MarkRead(ctx context.Context, recipient, id string) error
	MarkAllRead(ctx context.Context, recipient string) (int64, error)
}

// Streamer lets a stream wait for a recipient's notifications to change. It
// returns a channel that is signalled on each change and a cancel to release
// the subscription. *notifstream.Hub implements it.
type Streamer interface {
	Subscribe(recipient string) (<-chan struct{}, func())
}

// streamHeartbeat bounds how long a stream stays silent. A comment line keeps
// the connection, and any proxy between the client and the service, from idling
// the stream out.
const streamHeartbeat = 25 * time.Second

type Handler struct {
	service Service
	stream  Streamer
	auth    *auth.Middleware
	logger  *slog.Logger
}

func NewHandler(service Service, stream Streamer, middleware *auth.Middleware, logger *slog.Logger) Handler {
	if service == nil || stream == nil || middleware == nil {
		panic("notificationapi: service, streamer and auth middleware are required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return Handler{service: service, stream: stream, auth: middleware, logger: logger}
}

func (h Handler) RegisterRoutes(router *http.ServeMux) {
	read := h.auth.Require(auth.User, auth.ReadOwnNotifications)
	write := h.auth.Require(auth.User, auth.WriteOwnNotifications)
	router.Handle("GET /v1/notifications", read(http.HandlerFunc(h.list)))
	router.Handle("GET /v1/notifications/unread-count", read(http.HandlerFunc(h.unreadCount)))
	router.Handle("GET /v1/notifications/stream", read(http.HandlerFunc(h.streamUnread)))
	router.Handle("POST /v1/notifications/{id}/read", write(http.HandlerFunc(h.markRead)))
	router.Handle("POST /v1/notifications/read-all", write(http.HandlerFunc(h.markAllRead)))
}

type notificationResponse struct {
	ID        string     `json:"id"`
	ErrandID  string     `json:"errandId"`
	Type      string     `json:"type"`
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	CreatedAt time.Time  `json:"createdAt"`
	Read      bool       `json:"read"`
	ReadAt    *time.Time `json:"readAt,omitempty"`
}

type listResponse struct {
	Items      []notificationResponse `json:"items"`
	NextCursor string                 `json:"nextCursor,omitempty"`
}

func toResponse(n notification.Notification) notificationResponse {
	resp := notificationResponse{
		ID:        n.ID,
		ErrandID:  n.ErrandID,
		Type:      n.Type,
		Title:     n.Title,
		Body:      n.Body,
		CreatedAt: n.CreatedAt.UTC(),
		Read:      n.ReadAt != nil,
	}
	if n.ReadAt != nil {
		at := n.ReadAt.UTC()
		resp.ReadAt = &at
	}
	return resp
}

func (h Handler) list(w http.ResponseWriter, r *http.Request) {
	subject, ok := subjectOf(r)
	if !ok {
		h.fail(w, r, errors.New("missing principal"))
		return
	}
	opts := notification.ListOptions{
		Cursor:     r.URL.Query().Get("cursor"),
		UnreadOnly: r.URL.Query().Get("unread") == "true",
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 {
			writeError(w, http.StatusBadRequest, apperror.InvalidArgument, "limit must be a positive integer")
			return
		}
		opts.Limit = limit
	}

	items, nextCursor, err := h.service.List(r.Context(), subject, opts)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := listResponse{Items: make([]notificationResponse, 0, len(items)), NextCursor: nextCursor}
	for _, n := range items {
		out.Items = append(out.Items, toResponse(n))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h Handler) unreadCount(w http.ResponseWriter, r *http.Request) {
	subject, ok := subjectOf(r)
	if !ok {
		h.fail(w, r, errors.New("missing principal"))
		return
	}
	count, err := h.service.UnreadCount(r.Context(), subject)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"count": count})
}

// streamUnread holds the connection open and pushes the caller's unread count
// as Server-Sent Events: once on connect, then whenever one of their
// notifications is recorded. This is the push replacement for polling the
// unread-count route. The token arrives in the Authorization header like every
// other route, so nothing sensitive is placed in the URL.
func (h Handler) streamUnread(w http.ResponseWriter, r *http.Request) {
	subject, ok := subjectOf(r)
	if !ok {
		h.fail(w, r, errors.New("missing principal"))
		return
	}
	// The stream can only work if the response can be flushed to the client as
	// each event is written rather than buffered until the handler returns.
	rc := http.NewResponseController(w)
	if err := rc.Flush(); err != nil {
		writeError(w, http.StatusInternalServerError, apperror.Internal, "streaming is not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Tell nginx and similar proxies not to buffer the stream.
	w.Header().Set("X-Accel-Buffering", "no")

	signals, cancel := h.stream.Subscribe(subject)
	defer cancel()

	// Send the current count straight away so a freshly connected client shows
	// the right badge without waiting for the next event.
	if !h.sendUnread(w, rc, r, subject) {
		return
	}

	heartbeat := time.NewTicker(streamHeartbeat)
	defer heartbeat.Stop()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			if _, err := io.WriteString(w, ": keep-alive\n\n"); err != nil {
				return
			}
			if rc.Flush() != nil {
				return
			}
		case <-signals:
			if !h.sendUnread(w, rc, r, subject) {
				return
			}
		}
	}
}

// sendUnread writes the recipient's current unread count as one SSE event and
// flushes it. It reports whether the stream is still writable; a failed read of
// the count keeps the stream open (the next event or heartbeat retries) rather
// than dropping the client over a transient database blip.
func (h Handler) sendUnread(w http.ResponseWriter, rc *http.ResponseController, r *http.Request, subject string) bool {
	count, err := h.service.UnreadCount(r.Context(), subject)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "notification stream unread query failed", "error", err.Error())
		return true
	}
	if _, err := fmt.Fprintf(w, "event: unread\ndata: {\"count\":%d}\n\n", count); err != nil {
		return false
	}
	return rc.Flush() == nil
}

func (h Handler) markRead(w http.ResponseWriter, r *http.Request) {
	subject, ok := subjectOf(r)
	if !ok {
		h.fail(w, r, errors.New("missing principal"))
		return
	}
	if err := h.service.MarkRead(r.Context(), subject, r.PathValue("id")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h Handler) markAllRead(w http.ResponseWriter, r *http.Request) {
	subject, ok := subjectOf(r)
	if !ok {
		h.fail(w, r, errors.New("missing principal"))
		return
	}
	updated, err := h.service.MarkAllRead(r.Context(), subject)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"updated": updated})
}

func subjectOf(r *http.Request) (string, bool) {
	principal, ok := auth.PrincipalFromContext(r.Context())
	if !ok || principal.Subject == "" {
		return "", false
	}
	return principal.Subject, true
}

// fail maps use-case errors to stable responses. Unexpected errors are logged
// without request data and never shown to the caller.
func (h Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, notification.ErrInvalid):
		writeError(w, http.StatusBadRequest, apperror.InvalidArgument, "the request could not be processed")
	case errors.Is(err, notification.ErrNotFound):
		writeError(w, http.StatusNotFound, apperror.NotificationNotFound, "notification not found")
	default:
		h.logger.ErrorContext(r.Context(), "notification request failed", "error", err.Error(), "method", r.Method)
		writeError(w, http.StatusInternalServerError, apperror.Internal, "an unexpected internal error occurred")
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, code apperror.Code, message string) {
	writeJSON(w, status, apperror.Error{Code: code, Message: message})
}
