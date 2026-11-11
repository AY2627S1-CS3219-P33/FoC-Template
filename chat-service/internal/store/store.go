// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-10
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

// Package store is the only package that runs SQL. It wraps the
// sqlc-generated code in internal/database/chatgen, runs transactions, and
// converts rows into chat types and database errors into chat errors, so
// callers never see a pgx type or a raw database error.
package store

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/chat"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/database/chatgen"
)

// DB is what the store needs from a connection: a *pgxpool.Pool in the
// service, or a single *pgx.Conn.
type DB interface {
	chatgen.DBTX
	BeginTx(ctx context.Context, options pgx.TxOptions) (pgx.Tx, error)
}

// Store runs each query in its own implicit transaction; WithTx groups
// several into one. It is safe for concurrent use when its DB is.
type Store struct {
	Queries
	db DB
}

// Queries holds every query. Store's copy runs each on its own; the copy
// WithTx passes runs them inside the transaction.
type Queries struct {
	q *chatgen.Queries
}

// The compiler checks that Store has every method chat.Service needs.
var (
	_ chat.Store          = (*Store)(nil)
	_ chat.LifecycleStore = (*Store)(nil)
	_ chat.Tx             = (*Queries)(nil)
)

func New(db DB) *Store {
	if db == nil {
		panic("store: database is required")
	}
	return &Store{Queries: Queries{q: chatgen.New(db)}, db: db}
}

// WithTx runs fn in one READ COMMITTED transaction and commits when fn
// returns nil. Any error, or a panic, rolls it back. Publish to browsers only
// after WithTx returns nil: then the data is committed.
func (s *Store) WithTx(ctx context.Context, fn func(q *Queries) error) error {
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return translate("begin transaction", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := fn(&Queries{q: s.q.WithTx(tx)}); err != nil {
		return err
	}
	return translate("commit", tx.Commit(ctx))
}

// InTx is WithTx for package chat, which cannot name *Queries: chat imports
// nothing from store, so it describes the methods it needs as chat.Tx.
func (s *Store) InTx(ctx context.Context, fn func(tx chat.Tx) error) error {
	return s.WithTx(ctx, func(q *Queries) error { return fn(q) })
}

// MaxRowLimit bounds every list query.
const MaxRowLimit = 1_000

// rowLimit checks a page size.
func rowLimit(limit int) (int32, error) {
	if limit < 1 || limit > MaxRowLimit {
		return 0, fmt.Errorf("store: limit must be between 1 and %d", MaxRowLimit)
	}
	return int32(min(limit, math.MaxInt32)), nil
}

// SQLSTATE codes the store maps to chat errors.
const (
	foreignKeyViolation = "23503"
	checkViolation      = "23514"
)

// translate turns a pgx error into a chat error. Known rule violations become
// chat.ErrInvalidMessage, chat.ErrInvalidRoom or chat.ErrRoomNotFound. Other
// rejected data is a permanent error, and anything else (connection lost,
// timeout) wraps chat.ErrUnavailable so a caller may retry. The result never
// carries the database's own message, which can include row values.
func translate(op string, err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case pgErr.Code == checkViolation && pgErr.TableName == "chat_messages":
			return fmt.Errorf("store: %s: %w (%s)", op, chat.ErrInvalidMessage, pgErr.ConstraintName)
		case pgErr.Code == checkViolation && (pgErr.TableName == "chat_rooms" || pgErr.TableName == "errand_endings"):
			return fmt.Errorf("store: %s: %w (%s)", op, chat.ErrInvalidRoom, pgErr.ConstraintName)
		case pgErr.Code == foreignKeyViolation:
			return fmt.Errorf("store: %s: %w", op, chat.ErrRoomNotFound)
		case len(pgErr.Code) == 5 && (pgErr.Code[:2] == "22" || pgErr.Code[:2] == "23"):
			// Data exception or integrity violation: retrying cannot help.
			return fmt.Errorf("store: %s: rejected by the database (SQLSTATE %s)", op, pgErr.Code)
		}
		return &unavailableError{op: op, detail: "database error (SQLSTATE " + pgErr.Code + ")"}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		ctxErr := context.Canceled
		if errors.Is(err, context.DeadlineExceeded) {
			ctxErr = context.DeadlineExceeded
		}
		return &unavailableError{op: op, detail: ctxErr.Error(), ctxErr: ctxErr}
	}
	// Connection errors can name the host and user, so the text is dropped.
	return &unavailableError{op: op, detail: "database unreachable"}
}

// unavailableError is a failure a retry may fix. It matches
// chat.ErrUnavailable, and context.Canceled or context.DeadlineExceeded when
// that was the cause.
type unavailableError struct {
	op     string
	detail string
	ctxErr error
}

func (e *unavailableError) Error() string {
	return "store: " + e.op + ": " + e.detail
}

func (e *unavailableError) Unwrap() []error {
	if e.ctxErr != nil {
		return []error{chat.ErrUnavailable, e.ctxErr}
	}
	return []error{chat.ErrUnavailable}
}

func roomFromRow(row chatgen.ChatRoom) chat.Room {
	room := chat.Room{
		ID:          row.ID,
		ErrandID:    row.ErrandID,
		RequesterID: row.RequesterID,
		CourierID:   row.CourierID,
		CreatedAt:   row.CreatedAt,
	}
	if row.OpenedVersion != nil {
		version := int(*row.OpenedVersion)
		room.OpenedVersion = &version
	}
	// chat_rooms_end_fields guarantees the three are set together.
	if row.EndedAt != nil && row.EndReason != nil && row.ClosesAt != nil {
		room.Ending = &chat.Ending{
			Reason:   chat.EndReason(*row.EndReason),
			EndedAt:  *row.EndedAt,
			ClosesAt: *row.ClosesAt,
		}
	}
	return room
}

func roomsFromRows(rows []chatgen.ChatRoom) []chat.Room {
	rooms := make([]chat.Room, len(rows))
	for i, row := range rows {
		rooms[i] = roomFromRow(row)
	}
	return rooms
}

func messageFromRow(row chatgen.ChatMessage) chat.Message {
	return chat.Message{
		ID:          row.ID,
		RoomID:      row.RoomID,
		SenderID:    row.SenderID,
		Body:        row.Body,
		ClientMsgID: row.ClientMsgID,
		CreatedAt:   row.CreatedAt,
	}
}

func messagesFromRows(rows []chatgen.ChatMessage) []chat.Message {
	messages := make([]chat.Message, len(rows))
	for i, row := range rows {
		messages[i] = messageFromRow(row)
	}
	return messages
}
