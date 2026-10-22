// Package outboxadmin exposes audited replay of failed outbox messages.
package outboxadmin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/msgrepo"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store keeps replay and its audit atomic. PostgresStore implements it.
type Store interface {
	ListFailed(context.Context, int, int64) ([]msgrepo.Record, error)
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
func (s *PostgresStore) Requeue(ctx context.Context, actor string, id int64, reason string) (msgrepo.Record, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return msgrepo.Record{}, err
	}
	defer tx.Rollback(ctx)
	repo := msgrepo.New(tx)
	row, replayErr := repo.Requeue(ctx, id)
	if replayErr != nil && !errors.Is(replayErr, msgrepo.ErrNotFound) && !errors.Is(replayErr, msgrepo.ErrNotFailed) {
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
	store  Store
	guard  func(http.Handler) http.Handler
	logger *slog.Logger
}

func NewHandler(store Store, guard *auth.Middleware, logger *slog.Logger) Handler {
	if store == nil || guard == nil || logger == nil {
		panic("outboxadmin: dependencies are required")
	}
	return Handler{store, guard.RequireReview, logger}
}
func (h Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.Handle("GET /admin/outbox/failed", h.guard(http.HandlerFunc(h.list)))
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
func (h Handler) list(w http.ResponseWriter, r *http.Request) {
	limit := 25
	after := int64(0)
	q := r.URL.Query()
	if q.Has("limit") {
		n, err := strconv.Atoi(q.Get("limit"))
		if err != nil || n < 1 || n > 100 {
			auth.WriteError(w, apperror.InvalidArgument, "invalid limit")
			return
		}
		limit = n
	}
	if q.Has("cursor") {
		n, err := strconv.ParseInt(q.Get("cursor"), 10, 64)
		if err != nil || n < 1 {
			auth.WriteError(w, apperror.InvalidArgument, "invalid cursor")
			return
		}
		after = n
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
	if errors.Is(err, msgrepo.ErrNotFailed) {
		auth.WriteError(w, apperror.InvalidTransition, "outbox row is not FAILED")
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
