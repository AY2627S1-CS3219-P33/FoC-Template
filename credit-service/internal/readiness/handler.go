// Package readiness exposes the service readiness route.
package readiness

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Checker interface {
	Check(context.Context) error
}

// CheckerFunc adapts a function to a Checker.
type CheckerFunc func(context.Context) error

func (f CheckerFunc) Check(ctx context.Context) error { return f(ctx) }

// All is ready only while every checker is. It stops at the first failure.
func All(checkers ...Checker) Checker {
	return CheckerFunc(func(ctx context.Context) error {
		for _, checker := range checkers {
			if err := checker.Check(ctx); err != nil {
				return err
			}
		}
		return nil
	})
}

type Handler struct {
	checker Checker
}

func NewHandler(checker Checker) Handler {
	if checker == nil {
		panic("readiness: checker is required")
	}
	return Handler{checker: checker}
}

func (h Handler) RegisterRoutes(router *http.ServeMux) {
	router.HandleFunc("GET /readyz", h.get)
}

func (h Handler) get(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-Type", "application/json")
	if err := h.checker.Check(request.Context()); err != nil {
		response.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(response).Encode(map[string]string{"status": "not_ready"})
		return
	}
	_ = json.NewEncoder(response).Encode(map[string]string{"status": "ready"})
}

// PostgresChecker reports ready only while the database answers and its
// schema is at least the version this build expects. Migrations are applied
// out of band, so a stale schema must keep the instance out of rotation.
type PostgresChecker struct {
	pool            *pgxpool.Pool
	requiredVersion int64
}

func NewPostgresChecker(pool *pgxpool.Pool, requiredVersion int64) *PostgresChecker {
	if pool == nil {
		panic("readiness: PostgreSQL pool is required")
	}
	return &PostgresChecker{pool: pool, requiredVersion: requiredVersion}
}

func (c *PostgresChecker) Check(ctx context.Context) error {
	if err := c.pool.Ping(ctx); err != nil {
		return errors.New("database is unavailable")
	}
	var applied int64
	err := c.pool.QueryRow(ctx,
		`SELECT COALESCE(MAX(version_id), 0) FROM goose_db_version WHERE is_applied`,
	).Scan(&applied)
	if err != nil {
		return errors.New("schema version is unavailable")
	}
	if applied < c.requiredVersion {
		return errors.New("database schema is behind this build")
	}
	return nil
}
