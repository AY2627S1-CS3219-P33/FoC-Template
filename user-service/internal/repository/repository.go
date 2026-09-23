// Package repository owns persistence contracts and PostgreSQL adapters.
package repository

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("account not found")
	ErrConflict = errors.New("username or email is already registered")
	ErrInactive = errors.New("account is inactive")
)

// User contains account data, never credentials. Deleted accounts are excluded
// from ordinary reads and updates.
type User struct {
	ID          string
	Username    string
	Email       string
	Role        string
	Active      bool
	DisplayName string
	Mobile      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// CreateUser is persistence input. PasswordHash must be an encoded secure hash.
// Role and activation cannot be chosen by the caller.
type CreateUser struct {
	Username     string
	Email        string
	PasswordHash string `json:"-"`
}

// ProfilePatch supports omitted fields (nil) and explicit clearing (empty string).
// System-managed fields are deliberately absent.
type ProfilePatch struct {
	DisplayName *string
	Mobile      *string
}

type UserRepository interface {
	Create(ctx context.Context, input CreateUser) (*User, error)
	FindByID(ctx context.Context, id string) (*User, error)
	UpdateProfile(ctx context.Context, id string, patch ProfilePatch) (*User, error)
	// SoftDelete is a persistence primitive, not authorization to delete an account.
	// Call only after coordinated eligibility checks and session invalidation.
	SoftDelete(ctx context.Context, id string) error
}
