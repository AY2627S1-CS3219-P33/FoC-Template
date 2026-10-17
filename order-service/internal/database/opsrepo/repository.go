// Package opsrepo stores Person B's operational data in PostgreSQL: supplier
// deletion fences, open reviews, the admin audit log, settings and reminder
// outbox rows. It is a thin layer over opsgen that speaks in domain types and
// sentinel errors. It reads errands but never writes them.
package opsrepo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/opsgen"
)

var (
	// ErrNotFound means the row, or the errand it refers to, does not exist.
	ErrNotFound = errors.New("opsrepo: not found")
	// ErrConflict means a unique constraint rejected the write.
	ErrConflict = errors.New("opsrepo: conflicting row exists")
	// ErrInvalidArgument means an argument is malformed (a bad UUID, an
	// unknown enum value, a non-positive limit) or a CHECK constraint
	// rejected the row.
	ErrInvalidArgument = errors.New("opsrepo: invalid argument")
	// ErrFenceSupplierMismatch means the operation ID already names a fence
	// for another supplier.
	ErrFenceSupplierMismatch = errors.New("opsrepo: fence belongs to another supplier")
	// ErrFenceStateConflict means the fence is already in the other terminal
	// state (committing a released fence, or releasing a committed one).
	ErrFenceStateConflict = errors.New("opsrepo: fence is in a conflicting state")
)

// MaxPageSize bounds every list query.
const MaxPageSize = 200

// DB is what the repository needs from a connection: a *pgxpool.Pool in the
// service, or a single *pgx.Conn.
type DB interface {
	opsgen.DBTX
	BeginTx(ctx context.Context, options pgx.TxOptions) (pgx.Tx, error)
}

// Repository is safe for concurrent use when its DB is. A Repository passed
// to an InTx callback is bound to that transaction and must not outlive it.
type Repository struct {
	pool    DB
	queries *opsgen.Queries
}

func New(pool DB) *Repository {
	return &Repository{pool: pool, queries: opsgen.New(pool)}
}

// InTx runs fn in one READ COMMITTED transaction (the isolation the fence
// protocol in docs/contracts.md requires) and commits when fn returns nil.
// Called on a Repository that is already in a transaction, it runs fn in
// that transaction.
func (r *Repository) InTx(ctx context.Context, fn func(tx *Repository) error) error {
	if r.pool == nil {
		return fn(r)
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := fn(&Repository{queries: r.queries.WithTx(tx)}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// mapError turns pgx and PostgreSQL errors into this package's sentinels,
// keeping the original error in the chain.
func mapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %w", ErrNotFound, err)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // unique_violation
			return fmt.Errorf("%w: %s: %w", ErrConflict, pgErr.ConstraintName, err)
		case "23503": // foreign_key_violation
			return fmt.Errorf("%w: %s: %w", ErrNotFound, pgErr.ConstraintName, err)
		case "23514", "22P02": // check_violation, invalid_text_representation
			return fmt.Errorf("%w: %w", ErrInvalidArgument, err)
		}
	}
	return err
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidArgument, fmt.Sprintf(format, args...))
}

func parseUUID(name, value string) (pgtype.UUID, error) {
	var id pgtype.UUID
	if err := id.Scan(value); err != nil {
		return pgtype.UUID{}, invalid("%s is not a UUID", name)
	}
	return id, nil
}

func checkLimit(limit int) error {
	if limit <= 0 || limit > MaxPageSize {
		return invalid("limit must be between 1 and %d", MaxPageSize)
	}
	return nil
}

func uuidString(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	value, _ := id.Value()
	s, _ := value.(string)
	return s
}

func text(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: value != ""}
}

func timestamp(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value, Valid: !value.IsZero()}
}

func timeOrZero(value pgtype.Timestamptz) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return value.Time
}

func timePtr(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	t := value.Time
	return &t
}
