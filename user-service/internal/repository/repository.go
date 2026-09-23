// Package repository owns persistence contracts and PostgreSQL adapters.
package repository

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("account not found")
	ErrConflict = errors.New("email or external identity is already registered")
	ErrInactive = errors.New("account is inactive")
)

// User contains account data, never credentials. Deleted accounts are excluded
// from ordinary reads and updates.
type User struct {
	ID           string
	Auth0Subject string
	Username     string
	Email        string
	Role         string
	Active       bool
	DisplayName  string
	Mobile       string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// CreateUser is persistence input. PasswordHash must be an encoded secure hash.
// Role and activation cannot be chosen by the caller.
type CreateUser struct {
	Username     string
	Email        string
	PasswordHash string `json:"-"`
}

// CreateAuth0User is trusted persistence input derived from a validated Auth0
// access token and the matching /userinfo response.
type CreateAuth0User struct {
	Subject     string
	Username    string
	Email       string
	DisplayName string
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

// Auth0UserRepository owns the external-identity lookup and provisioning path.
type Auth0UserRepository interface {
	FindByAuth0Subject(ctx context.Context, subject string) (*User, error)
	CreateAuth0(ctx context.Context, input CreateAuth0User) (*User, bool, error)
}
