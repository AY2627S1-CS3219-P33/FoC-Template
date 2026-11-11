// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: Implementation of notification-service PostgreSQL connection management.
// Author review: I validated correctness, edited for style, and added boundary checks.

// Package notifrepo is the PostgreSQL implementation of notification.Store.
// Reads are always scoped to one recipient. Writes are idempotent on
// (message_id, recipient), so a redelivered event adds nothing.
package notifrepo

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CS3219-AY2627S1/FoC-Template/notification-service/internal/notification"
)

type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) Store {
	if pool == nil {
		panic("notifrepo: pool is required")
	}
	return Store{pool: pool}
}

// Insert writes one row per item in a single transaction, skipping any
// (message_id, recipient) pair already stored. It returns how many rows were
// newly inserted.
func (s Store) Insert(ctx context.Context, messageID string, items []notification.Pending) (int, error) {
	if len(items) == 0 {
		return 0, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const insert = `
		INSERT INTO notifications (message_id, recipient, errand_id, type, title, body, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (message_id, recipient) DO NOTHING`

	inserted := 0
	for _, item := range items {
		tag, err := tx.Exec(ctx, insert,
			messageID, item.Recipient, item.ErrandID, item.Type, item.Title, item.Body, item.CreatedAt.UTC())
		if err != nil {
			return 0, err
		}
		inserted += int(tag.RowsAffected())
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return inserted, nil
}

func (s Store) List(ctx context.Context, recipient string, opts notification.ListOptions) ([]notification.Notification, string, error) {
	// A stable total order (created_at, id) lets the cursor resume exactly
	// where the last page stopped even when timestamps tie.
	args := []any{recipient, opts.Limit + 1}
	where := []string{"recipient = $1"}
	if opts.UnreadOnly {
		where = append(where, "read_at IS NULL")
	}
	if opts.Cursor != "" {
		createdAt, id, err := decodeCursor(opts.Cursor)
		if err != nil {
			return nil, "", notification.ErrInvalid
		}
		args = append(args, createdAt, id)
		where = append(where, "(created_at, id) < ($3, $4)")
	}

	query := `
		SELECT id, recipient, errand_id, type, title, body, created_at, read_at
		FROM notifications
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY created_at DESC, id DESC
		LIMIT $2`

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	items := make([]notification.Notification, 0, opts.Limit)
	for rows.Next() {
		var n notification.Notification
		if err := rows.Scan(&n.ID, &n.Recipient, &n.ErrandID, &n.Type, &n.Title, &n.Body, &n.CreatedAt, &n.ReadAt); err != nil {
			return nil, "", err
		}
		items = append(items, n)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	// The extra row asked for signals more pages without a second query.
	var nextCursor string
	if len(items) > opts.Limit {
		last := items[opts.Limit-1]
		nextCursor = encodeCursor(last.CreatedAt, last.ID)
		items = items[:opts.Limit]
	}
	return items, nextCursor, nil
}

func (s Store) UnreadCount(ctx context.Context, recipient string) (int64, error) {
	var count int64
	err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM notifications WHERE recipient = $1 AND read_at IS NULL`,
		recipient,
	).Scan(&count)
	return count, err
}

func (s Store) MarkRead(ctx context.Context, recipient, id string) (bool, error) {
	// The id is compared as uuid, so a malformed id is a clean not-found
	// rather than a database error.
	tag, err := s.pool.Exec(ctx,
		`UPDATE notifications SET read_at = now()
		 WHERE recipient = $1 AND id = $2::uuid AND read_at IS NULL`,
		recipient, id)
	if err != nil {
		if isInvalidUUID(err) {
			return false, nil
		}
		return false, err
	}
	if tag.RowsAffected() > 0 {
		return true, nil
	}
	// Distinguish "already read" (exists, so found) from "does not exist".
	var exists bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM notifications WHERE recipient = $1 AND id = $2::uuid)`,
		recipient, id,
	).Scan(&exists); err != nil {
		if isInvalidUUID(err) {
			return false, nil
		}
		return false, err
	}
	return exists, nil
}

func (s Store) MarkAllRead(ctx context.Context, recipient string) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE notifications SET read_at = now() WHERE recipient = $1 AND read_at IS NULL`,
		recipient)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func encodeCursor(createdAt time.Time, id string) string {
	raw := createdAt.UTC().Format(time.RFC3339Nano) + "|" + id
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(cursor string) (time.Time, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", err
	}
	at, id, found := strings.Cut(string(raw), "|")
	if !found || id == "" {
		return time.Time{}, "", errors.New("malformed cursor")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return time.Time{}, "", err
	}
	return createdAt, id, nil
}

// isInvalidUUID reports whether err is PostgreSQL's invalid-uuid error (22P02),
// which a bad id path parameter triggers.
func isInvalidUUID(err error) bool {
	var pgErr interface{ SQLState() string }
	return errors.As(err, &pgErr) && pgErr.SQLState() == "22P02"
}
