// Package errandrepo is the PostgreSQL errand.Store. It is the only writer of
// errands and errand_transitions, and it writes the outbox and errand_reviews
// rows a transition emits in the same READ COMMITTED transaction as the
// errand row (docs/contracts.md). It is a thin layer over errandgen: the
// decisions are errand.Next's, and the store only persists them.
package errandrepo

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/errandgen"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

// DB is what the store needs from a connection: a *pgxpool.Pool in the
// service, or a single *pgx.Conn.
type DB interface {
	errandgen.DBTX
	BeginTx(ctx context.Context, options pgx.TxOptions) (pgx.Tx, error)
}

// Store is safe for concurrent use when its DB is.
type Store struct {
	db      DB
	queries *errandgen.Queries
}

var _ errand.Store = (*Store)(nil)

func New(db DB) *Store {
	return &Store{db: db, queries: errandgen.New(db)}
}

// inTx runs fn in one READ COMMITTED transaction, the isolation the fence
// protocol in docs/contracts.md requires, and commits when fn returns nil.
func (s *Store) inTx(ctx context.Context, fn func(q *errandgen.Queries) error) error {
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := fn(s.queries.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func appErr(code apperror.Code, format string, args ...any) error {
	return &apperror.Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

func notFound(id string) error {
	return appErr(apperror.ErrandNotFound, "errand %q not found", id)
}

// parseUUID parses a UUID argument; a malformed one is INVALID_ARGUMENT.
func parseUUID(name, value string) (pgtype.UUID, error) {
	id, ok := toUUID(value)
	if !ok {
		return pgtype.UUID{}, appErr(apperror.InvalidArgument, "%s %q is not a UUID", name, value)
	}
	return id, nil
}

// toUUID parses value, reporting false for a malformed one. Lookups by a
// malformed errand ID find nothing rather than fail.
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

func checkLimit(limit int) error {
	if limit < 1 || limit > errand.MaxListLimit {
		return appErr(apperror.InvalidArgument, "limit must be between 1 and %d", errand.MaxListLimit)
	}
	return nil
}

// rowLimit converts a positive limit, clamping it to the int32 range.
func rowLimit(limit int) int32 {
	return int32(min(limit, math.MaxInt32))
}

// text is a nullable text column; "" is NULL.
func text(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: value != ""}
}

// timestamp is a non-null timestamptz column.
func timestamp(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value, Valid: true}
}

// nullTimestamp is a nullable timestamptz column; nil is NULL.
func nullTimestamp(value *time.Time) pgtype.Timestamptz {
	if value == nil {
		return pgtype.Timestamptz{}
	}
	return timestamp(*value)
}

func timePtr(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	t := value.Time
	return &t
}

func statusesWhere(keep func(errand.Status) bool) []string {
	var statuses []string
	for _, s := range errand.Statuses() {
		if keep(s) {
			statuses = append(statuses, string(s))
		}
	}
	return statuses
}

func activeStatuses() []string { return statusesWhere(errand.Status.Active) }

func terminalStatuses() []string { return statusesWhere(errand.Status.Terminal) }
