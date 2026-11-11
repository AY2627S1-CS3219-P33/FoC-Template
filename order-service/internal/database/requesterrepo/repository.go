// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-09
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

// Package requesterrepo stores the requester enhancements in PostgreSQL:
// saved places now (places.Store), scheduled errands later. It is a thin
// layer over requestergen that speaks in domain types and apperror codes. It
// never reads or writes errands.
package requesterrepo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/requestergen"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/places"
)

// DB is what the repository needs from a connection: a *pgxpool.Pool in the
// service, or a single *pgx.Conn.
type DB interface {
	requestergen.DBTX
	BeginTx(ctx context.Context, options pgx.TxOptions) (pgx.Tx, error)
}

// Repository is safe for concurrent use when its DB is.
type Repository struct {
	db      DB
	queries *requestergen.Queries
}

var _ places.Store = (*Repository)(nil)

func New(db DB) *Repository {
	return &Repository{db: db, queries: requestergen.New(db)}
}

// inTx runs fn in one READ COMMITTED transaction and commits when fn
// returns nil.
func (r *Repository) inTx(ctx context.Context, fn func(q *requestergen.Queries) error) error {
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := fn(r.queries.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// uniqueViolation returns the name of the constraint a unique violation
// hit, or "" for any other error.
func uniqueViolation(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return pgErr.ConstraintName
	}
	return ""
}

// parseUUID parses a UUID argument; a malformed one is INVALID_ARGUMENT and
// "" is NULL.
func parseUUID(name, value string) (pgtype.UUID, error) {
	if value == "" {
		return pgtype.UUID{}, nil
	}
	id, ok := toUUID(value)
	if !ok {
		return pgtype.UUID{}, &apperror.Error{
			Code:    apperror.InvalidArgument,
			Message: fmt.Sprintf("%s is not a UUID", name),
		}
	}
	return id, nil
}

// toUUID parses value, reporting false for a malformed one. Lookups by a
// malformed place ID find nothing rather than fail.
func toUUID(value string) (pgtype.UUID, bool) {
	var id pgtype.UUID
	if err := id.Scan(value); err != nil {
		return pgtype.UUID{}, false
	}
	return id, true
}

func uuidString(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	value, _ := id.Value()
	s, _ := value.(string)
	return s
}

// text is a nullable text column; "" is NULL.
func text(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: value != ""}
}

// textPtr is a nullable text argument; nil is NULL.
func textPtr(value *string) pgtype.Text {
	if value == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *value, Valid: true}
}

func timeOrZero(value pgtype.Timestamptz) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return value.Time
}
