// Package outboxrepo holds the SQL for the result outbox: adding a result in
// the transaction of the balance change (Enqueue), and the queries a
// dispatcher uses to publish rows (Claim, MarkSent, Reschedule, MarkFailed).
package outboxrepo

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Row statuses.
const (
	StatusPending = "PENDING"
	StatusSent    = "SENT"
	StatusFailed  = "FAILED"
)

// DBTX is satisfied by pgx.Tx and *pgxpool.Pool.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Row is an outbox row as the dispatcher sees it.
type Row struct {
	ID         int64
	Key        string
	RoutingKey string
	Payload    json.RawMessage
	Status     string
	// Attempts counts claims, so a row being published has at least 1.
	Attempts int
}

// Enqueue adds a result message to publish. If a row with the key exists, its
// payload is kept, and it is set back to PENDING unless it already is: the
// command was delivered again, which means order-service may not have the
// result, so it is sent again. Duplicate results are harmless to the receiver.
func Enqueue(ctx context.Context, db DBTX, key, routingKey string, payload []byte) error {
	_, err := db.Exec(ctx,
		`INSERT INTO outbox (idempotency_key, routing_key, payload)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (idempotency_key) DO UPDATE
		   SET status = 'PENDING', attempts = 0, next_attempt_at = clock_timestamp(),
		       lease_owner = NULL, lease_expires_at = NULL, last_failure_code = NULL
		   WHERE outbox.status <> 'PENDING'`,
		key, routingKey, payload)
	return err
}

// Claim leases up to limit due PENDING rows to a worker and counts an attempt
// on each. A row is due when its next attempt time has passed and no worker
// holds an unexpired lease on it. Rows locked by another claim are skipped, so
// workers never get the same row. The rows are returned oldest first.
func Claim(ctx context.Context, db DBTX, worker string, limit int, lease time.Duration) ([]Row, error) {
	rows, err := db.Query(ctx,
		`WITH due AS (
		   SELECT id FROM outbox
		   WHERE status = 'PENDING' AND next_attempt_at <= clock_timestamp()
		     AND (lease_expires_at IS NULL OR lease_expires_at < clock_timestamp())
		   ORDER BY next_attempt_at, id
		   LIMIT $1
		   FOR UPDATE SKIP LOCKED)
		 UPDATE outbox o
		 SET lease_owner = $2,
		     lease_expires_at = clock_timestamp() + make_interval(secs => $3),
		     attempts = o.attempts + 1
		 FROM due WHERE o.id = due.id
		 RETURNING o.id, o.idempotency_key, o.routing_key, o.payload, o.status, o.attempts`,
		limit, worker, lease.Seconds())
	if err != nil {
		return nil, err
	}
	claimed, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Row, error) {
		var r Row
		err := row.Scan(&r.ID, &r.Key, &r.RoutingKey, &r.Payload, &r.Status, &r.Attempts)
		return r, err
	})
	slices.SortFunc(claimed, func(a, b Row) int { return int(a.ID - b.ID) })
	return claimed, err
}

// MarkSent records a confirmed publish. Like the next two functions it only
// changes a row the worker still holds the lease on, and reports whether it
// did: a worker that lost its lease changes nothing.
func MarkSent(ctx context.Context, db DBTX, id int64, worker string) (bool, error) {
	tag, err := db.Exec(ctx,
		`UPDATE outbox
		 SET status = 'SENT', sent_at = clock_timestamp(), last_failure_code = NULL,
		     lease_owner = NULL, lease_expires_at = NULL
		 WHERE id = $1 AND lease_owner = $2 AND status = 'PENDING'`, id, worker)
	return tag.RowsAffected() == 1, err
}

// Reschedule gives a row back after a failed publish, to be tried again after
// the delay. code says why, in a few words; it never holds error text.
func Reschedule(ctx context.Context, db DBTX, id int64, worker string, delay time.Duration, code string) (bool, error) {
	tag, err := db.Exec(ctx,
		`UPDATE outbox
		 SET next_attempt_at = clock_timestamp() + make_interval(secs => $3),
		     last_failure_code = $4, lease_owner = NULL, lease_expires_at = NULL
		 WHERE id = $1 AND lease_owner = $2 AND status = 'PENDING'`,
		id, worker, delay.Seconds(), code)
	return tag.RowsAffected() == 1, err
}

// MarkFailed stops retrying a row. A repeat of the command that caused the
// result puts it back to PENDING (see Enqueue).
func MarkFailed(ctx context.Context, db DBTX, id int64, worker, code string) (bool, error) {
	tag, err := db.Exec(ctx,
		`UPDATE outbox
		 SET status = 'FAILED', last_failure_code = $3,
		     lease_owner = NULL, lease_expires_at = NULL
		 WHERE id = $1 AND lease_owner = $2 AND status = 'PENDING'`, id, worker, code)
	return tag.RowsAffected() == 1, err
}

// Store is the dispatcher's view of the outbox on one database handle.
type Store struct{ DB DBTX }

func (s Store) Claim(ctx context.Context, worker string, limit int, lease time.Duration) ([]Row, error) {
	return Claim(ctx, s.DB, worker, limit, lease)
}

func (s Store) MarkSent(ctx context.Context, id int64, worker string) (bool, error) {
	return MarkSent(ctx, s.DB, id, worker)
}

func (s Store) Reschedule(ctx context.Context, id int64, worker string, delay time.Duration, code string) (bool, error) {
	return Reschedule(ctx, s.DB, id, worker, delay, code)
}

func (s Store) MarkFailed(ctx context.Context, id int64, worker, code string) (bool, error) {
	return MarkFailed(ctx, s.DB, id, worker, code)
}
