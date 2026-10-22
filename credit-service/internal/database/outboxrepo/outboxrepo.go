// Package outboxrepo holds the SQL for the result outbox. This file has the
// part command handling needs: adding a result inside the transaction of the
// balance change. Claiming and publishing rows belongs to the dispatcher.
package outboxrepo

import (
	"context"

	"github.com/jackc/pgx/v5/pgconn"
)

// DBTX is satisfied by pgx.Tx and *pgxpool.Pool.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
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
