package walletrepo

import (
	"context"
	"encoding/json"
	"errors"
)

// LockCommand serialises work on one command message ID until the transaction
// ends, so two deliveries of the same message cannot both run.
func LockCommand(ctx context.Context, db DBTX, messageID string) error {
	_, err := db.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, messageID)
	return mapError(err)
}

type ProcessedCommand struct {
	MessageID  string
	ResultType string
	Result     json.RawMessage
}

// GetProcessedCommand returns the stored result of a command, or ErrNotFound
// if the message has not been applied.
func GetProcessedCommand(ctx context.Context, db DBTX, messageID string) (ProcessedCommand, error) {
	var c ProcessedCommand
	err := db.QueryRow(ctx,
		`SELECT message_id, result_type, result FROM processed_commands WHERE message_id = $1`,
		messageID).Scan(&c.MessageID, &c.ResultType, &c.Result)
	return c, mapError(err)
}

// InsertProcessedCommand records the result of a command. It returns
// ErrAlreadyExists if the message was recorded before.
func InsertProcessedCommand(ctx context.Context, db DBTX, c ProcessedCommand) error {
	_, err := db.Exec(ctx,
		`INSERT INTO processed_commands (message_id, result_type, result) VALUES ($1, $2, $3)`,
		c.MessageID, c.ResultType, []byte(c.Result))
	return mapError(err)
}

// InsertReleasedOrder records that the order was released before any
// reservation existed. It does nothing if the tombstone already exists.
func InsertReleasedOrder(ctx context.Context, db DBTX, orderID string) error {
	_, err := db.Exec(ctx,
		`INSERT INTO released_orders (order_id) VALUES ($1::uuid) ON CONFLICT DO NOTHING`, orderID)
	return mapError(err)
}

// IsOrderReleased reports whether a release tombstone exists for the order.
func IsOrderReleased(ctx context.Context, db DBTX, orderID string) (bool, error) {
	var one int
	err := db.QueryRow(ctx,
		`SELECT 1 FROM released_orders WHERE order_id = $1::uuid`, orderID).Scan(&one)
	if errors.Is(mapError(err), ErrNotFound) {
		return false, nil
	}
	return err == nil, mapError(err)
}
