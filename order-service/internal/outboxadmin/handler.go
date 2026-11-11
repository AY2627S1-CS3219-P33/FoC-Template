// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-10-22
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

// Package outboxadmin exposes audited replay of outbox messages, and the list
// of messages that are overdue for one.
package outboxadmin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/msgrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store keeps replay and its audit atomic. PostgresStore implements it.
type Store interface {
	ListFailed(context.Context, int, int64) ([]msgrepo.Record, error)
	ListOverdue(ctx context.Context, threshold time.Duration, status string, limit int, afterID int64) ([]msgrepo.Record, error)
	Requeue(context.Context, string, int64, string) (msgrepo.Record, error)
}
type PostgresStore struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *PostgresStore {
	if pool == nil {
		panic("outboxadmin: pool is required")
	}
	return &PostgresStore{pool}
}
func (s *PostgresStore) ListFailed(ctx context.Context, limit int, after int64) ([]msgrepo.Record, error) {
	return msgrepo.New(s.pool).ListFailed(ctx, limit, after)
}
func (s *PostgresStore) ListOverdue(ctx context.Context, threshold time.Duration, status string, limit int, afterID int64) ([]msgrepo.Record, error) {
	return msgrepo.New(s.pool).ListOverdue(ctx, threshold, status, limit, afterID)
}
func (s *PostgresStore) Requeue(ctx context.Context, actor string, id int64, reason string) (msgrepo.Record, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return msgrepo.Record{}, err
	}
	defer tx.Rollback(ctx)
	repo := msgrepo.New(tx)
	row, replayErr := repo.Requeue(ctx, id)
	if replayErr != nil && !errors.Is(replayErr, msgrepo.ErrNotFound) && !errors.Is(replayErr, msgrepo.ErrNotRequeueable) {
		return row, replayErr
	}
	outcome := msgrepo.AuditSucceeded
	if replayErr != nil {
		outcome = msgrepo.AuditRejected
	}
	if err = repo.InsertRequeueAudit(ctx, actor, id, reason, outcome); err != nil {
		return msgrepo.Record{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return msgrepo.Record{}, err
	}
	return row, replayErr
}

type Handler struct {
	store    Store
	settings errand.Settings
	guard    func(http.Handler) http.Handler
	logger   *slog.Logger
	now      func() time.Time
}

// NewHandler serves the outbox admin routes. settings supplies the default
// overdue threshold, outboxConfirmTimeout.
func NewHandler(store Store, settings errand.Settings, guard *auth.Middleware, logger *slog.Logger) Handler {
	if store == nil || settings == nil || guard == nil || logger == nil {
		panic("outboxadmin: dependencies are required")
	}
	return Handler{store, settings, guard.RequireReview, logger, time.Now}
}
func (h Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.Handle("GET /admin/outbox/failed", h.guard(http.HandlerFunc(h.list)))
	mux.Handle("GET /admin/outbox/overdue", h.guard(http.HandlerFunc(h.overdue)))
	mux.Handle("POST /admin/outbox/{id}/requeue", h.guard(http.HandlerFunc(h.requeue)))
}

type rowResponse struct {
	ID              int64      `json:"id"`
	Kind            string     `json:"kind"`
	ErrandID        string     `json:"errandId"`
	Status          string     `json:"status"`
	Attempts        int32      `json:"attempts"`
	LastFailureCode string     `json:"lastFailureCode,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	NextAttemptAt   time.Time  `json:"nextAttemptAt"`
	SentAt          *time.Time `json:"sentAt,omitempty"`
}

func project(r msgrepo.Record) rowResponse {
	out := rowResponse{ID: r.ID, Kind: r.Kind, ErrandID: r.ErrandID, Status: r.Status, Attempts: r.Attempts, LastFailureCode: r.LastFailureCode, CreatedAt: r.CreatedAt, NextAttemptAt: r.NextAttemptAt}
	if !r.SentAt.IsZero() {
		out.SentAt = &r.SentAt
	}
	return out
}
func write(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

// pageParams reads limit and cursor. It writes the error response and
// reports false when either is invalid.
func pageParams(w http.ResponseWriter, q url.Values) (limit int, after int64, ok bool) {
	limit = 25
	if q.Has("limit") {
		n, err := strconv.Atoi(q.Get("limit"))
		if err != nil || n < 1 || n > 100 {
			auth.WriteError(w, apperror.InvalidArgument, "invalid limit")
			return 0, 0, false
		}
		limit = n
	}
	if q.Has("cursor") {
		n, err := strconv.ParseInt(q.Get("cursor"), 10, 64)
		if err != nil || n < 1 {
			auth.WriteError(w, apperror.InvalidArgument, "invalid cursor")
			return 0, 0, false
		}
		after = n
	}
	return limit, after, true
}

func (h Handler) list(w http.ResponseWriter, r *http.Request) {
	limit, after, ok := pageParams(w, r.URL.Query())
	if !ok {
		return
	}
	rows, err := h.store.ListFailed(r.Context(), limit+1, after)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	page := struct {
		Items      []rowResponse `json:"items"`
		NextCursor string        `json:"nextCursor,omitempty"`
	}{Items: make([]rowResponse, 0, limit)}
	if len(rows) > limit {
		page.NextCursor = strconv.FormatInt(rows[limit-1].ID, 10)
		rows = rows[:limit]
	}
	for _, row := range rows {
		page.Items = append(page.Items, project(row))
	}
	write(w, page)
}

// overdueRow is a row plus how long it has waited: since it was sent for a
// SENT row, since it was created for a PENDING one.
type overdueRow struct {
	rowResponse
	AgeSeconds int64 `json:"ageSeconds"`
}

// overdue lists SENT credit commands still waiting for a result and PENDING
// rows the dispatcher has not published, older than olderThan seconds (default
// the outboxConfirmTimeout setting). Neither is retried by anyone else
// automatically: PENDING rows are retried by the dispatcher already, and a SENT
// command is held by the broker, so a human decides whether to requeue.
func (h Handler) overdue(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, after, ok := pageParams(w, q)
	if !ok {
		return
	}
	status := q.Get("status")
	if status != "" && status != msgrepo.StatusSent && status != msgrepo.StatusPending {
		auth.WriteError(w, apperror.InvalidArgument, "invalid status")
		return
	}
	var threshold time.Duration
	if q.Has("olderThan") {
		n, err := strconv.ParseInt(q.Get("olderThan"), 10, 64)
		if err != nil || n < 1 || n > math.MaxInt32 {
			auth.WriteError(w, apperror.InvalidArgument, "invalid olderThan")
			return
		}
		threshold = time.Duration(n) * time.Second
	} else {
		settings, err := h.settings.Get(r.Context())
		if err != nil {
			h.fail(w, r, err)
			return
		}
		threshold = settings.OutboxConfirmTimeout
	}
	rows, err := h.store.ListOverdue(r.Context(), threshold, status, limit+1, after)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	page := struct {
		ThresholdSeconds int64        `json:"thresholdSeconds"`
		Items            []overdueRow `json:"items"`
		NextCursor       string       `json:"nextCursor,omitempty"`
	}{ThresholdSeconds: int64(threshold / time.Second), Items: make([]overdueRow, 0, limit)}
	if len(rows) > limit {
		page.NextCursor = strconv.FormatInt(rows[limit-1].ID, 10)
		rows = rows[:limit]
	}
	now := h.now()
	for _, row := range rows {
		since := row.CreatedAt
		if row.Status == msgrepo.StatusSent {
			since = row.SentAt
		}
		page.Items = append(page.Items, overdueRow{rowResponse: project(row), AgeSeconds: max(0, int64(now.Sub(since)/time.Second))})
	}
	write(w, page)
}
func (h Handler) requeue(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		auth.WriteError(w, apperror.InvalidArgument, "invalid outbox row ID")
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	decoder.DisallowUnknownFields()
	err = decoder.Decode(&body)
	if err == nil {
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			err = errors.New("trailing data")
		}
	}
	if err != nil || strings.TrimSpace(body.Reason) == "" || utf8.RuneCountInString(body.Reason) > 500 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_ = json.NewEncoder(w).Encode(apperror.Error{Code: apperror.ValidationFailed, Message: "reason must be a non-blank string of at most 500 characters", Fields: []apperror.Field{{Field: "reason", Message: "must be a non-blank string of at most 500 characters"}}})
		return
	}
	actor, _ := auth.PrincipalFromContext(r.Context())
	row, err := h.store.Requeue(r.Context(), actor.Subject, id, body.Reason)
	if errors.Is(err, msgrepo.ErrNotFound) {
		auth.WriteError(w, apperror.OutboxRowNotFound, "outbox row not found")
		return
	}
	if errors.Is(err, msgrepo.ErrNotRequeueable) {
		auth.WriteError(w, apperror.InvalidTransition, "only a FAILED row or a SENT credit command can be requeued")
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	write(w, project(row))
}
func (h Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	h.logger.ErrorContext(r.Context(), "outbox administration failed", "error", err)
	auth.WriteError(w, apperror.Internal, "an unexpected internal error occurred")
}
