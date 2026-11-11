// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: Implementation of notification-service notification business logic.
// Author review: I validated correctness, edited for style, and added boundary checks.

package notification

import (
	"context"
	"errors"
	"strings"
)

// Sentinel errors the HTTP layer maps to stable status codes.
var (
	// ErrInvalid is a bad argument from the caller (empty id, bad limit).
	ErrInvalid = errors.New("invalid argument")
	// ErrNotFound means no notification with that id belongs to the caller.
	ErrNotFound = errors.New("notification not found")
)

// MaxPageSize bounds a single list page so one request cannot pull an
// unbounded history.
const (
	MaxPageSize     = 100
	DefaultPageSize = 20
)

// Store is the persistence boundary. message_id plus recipient is unique, so
// Insert is idempotent: a redelivered event stores nothing new. Reads are
// always scoped to one recipient; there is no way to read another user's
// notifications.
type Store interface {
	// Insert stores one row per item, ignoring any (messageID, recipient)
	// pair already present. It returns how many rows were newly inserted.
	Insert(ctx context.Context, messageID string, items []Pending) (int, error)
	// List returns a recipient's notifications, newest first. A non-empty
	// nextCursor is returned when more rows remain.
	List(ctx context.Context, recipient string, opts ListOptions) (items []Notification, nextCursor string, err error)
	// UnreadCount counts a recipient's unread notifications.
	UnreadCount(ctx context.Context, recipient string) (int64, error)
	// MarkRead marks one notification read, only if it belongs to recipient.
	// It reports whether a row was updated (false means not found or already
	// read for a row that exists is still true).
	MarkRead(ctx context.Context, recipient, id string) (found bool, err error)
	// MarkAllRead marks every unread notification of recipient read and
	// returns how many were changed.
	MarkAllRead(ctx context.Context, recipient string) (int64, error)
}

// ListOptions controls a list query. Cursor is opaque to callers.
type ListOptions struct {
	Limit      int
	Cursor     string
	UnreadOnly bool
}

// Service is the use-case boundary the HTTP handlers and the message handler
// depend on.
type Service struct {
	store Store
}

func NewService(store Store) *Service {
	if store == nil {
		panic("notification: store is required")
	}
	return &Service{store: store}
}

// Record persists the notifications derived from one event. messageID is the
// event's deduplication key, so a redelivery stores nothing new.
func (s *Service) Record(ctx context.Context, messageID string, items []Pending) (int, error) {
	if strings.TrimSpace(messageID) == "" {
		return 0, errors.New("notification: messageID is required")
	}
	if len(items) == 0 {
		return 0, nil
	}
	return s.store.Insert(ctx, messageID, items)
}

// List returns the caller's notifications. The limit is clamped to the
// allowed range so a caller cannot ask for an unbounded page.
func (s *Service) List(ctx context.Context, recipient string, opts ListOptions) ([]Notification, string, error) {
	if strings.TrimSpace(recipient) == "" {
		return nil, "", ErrInvalid
	}
	if opts.Limit <= 0 {
		opts.Limit = DefaultPageSize
	}
	if opts.Limit > MaxPageSize {
		opts.Limit = MaxPageSize
	}
	return s.store.List(ctx, recipient, opts)
}

// UnreadCount returns how many of the caller's notifications are unread.
func (s *Service) UnreadCount(ctx context.Context, recipient string) (int64, error) {
	if strings.TrimSpace(recipient) == "" {
		return 0, ErrInvalid
	}
	return s.store.UnreadCount(ctx, recipient)
}

// MarkRead marks one of the caller's notifications read.
func (s *Service) MarkRead(ctx context.Context, recipient, id string) error {
	if strings.TrimSpace(recipient) == "" || strings.TrimSpace(id) == "" {
		return ErrInvalid
	}
	found, err := s.store.MarkRead(ctx, recipient, id)
	if err != nil {
		return err
	}
	if !found {
		return ErrNotFound
	}
	return nil
}

// MarkAllRead marks all of the caller's notifications read and returns the
// number changed.
func (s *Service) MarkAllRead(ctx context.Context, recipient string) (int64, error) {
	if strings.TrimSpace(recipient) == "" {
		return 0, ErrInvalid
	}
	return s.store.MarkAllRead(ctx, recipient)
}
