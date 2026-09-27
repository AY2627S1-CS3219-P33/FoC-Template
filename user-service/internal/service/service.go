// Package service implements internal student-account business operations.
package service

import (
	"context"
	"errors"
	"unicode"
	"unicode/utf8"

	"user-service/internal/repository"
)

var (
	ErrValidation  = errors.New("invalid account input")
	ErrUnavailable = errors.New("required dependency is unavailable")
	ErrNotFound    = repository.ErrNotFound
	ErrConflict    = repository.ErrConflict
	ErrInactive    = repository.ErrInactive
)

// ValidationError never contains submitted values.
type ValidationError struct{ Field, Message string }

// Error returns the field name and validation message.
func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }

// Unwrap allows errors.Is to identify ErrValidation.
func (e *ValidationError) Unwrap() error { return ErrValidation }

// Profile contains no credentials. A nil CreditBalance means unavailable, not zero.
// The future credit adapter returns an exact decimal string, never a float.
type Profile struct {
	ID            string  `json:"id"`
	Username      string  `json:"username"`
	Email         string  `json:"email"`
	Role          string  `json:"role"`
	Active        bool    `json:"active"`
	DisplayName   string  `json:"display_name"`
	Mobile        string  `json:"mobile_number"`
	CreditBalance *string `json:"credit_balance"`
}

type ProfileUpdate struct {
	DisplayName *string
	Mobile      *string
}

// UserService has no public transport. For self-service operations the caller
// must obtain authenticatedUserID from real authentication, not user input.
type UserService struct {
	users    repository.UserRepository
	balances CreditBalanceReader
}

// NewUserService creates a user service with an optional credit balance reader.
func NewUserService(users repository.UserRepository, balances CreditBalanceReader) *UserService {
	return &UserService{users: users, balances: balances}
}

// GetProfile returns the authenticated account's profile if the account is active.
// An unavailable credit balance is represented by nil.
func (s *UserService) GetProfile(ctx context.Context, authenticatedUserID string) (*Profile, error) {
	u, err := s.activeAccount(ctx, authenticatedUserID)
	if err != nil {
		return nil, err
	}
	return s.withBalance(ctx, u), nil
}

// UpdateProfile validates and updates the supplied fields of an active account.
// Nil fields are left unchanged; empty strings clear the corresponding fields.
func (s *UserService) UpdateProfile(ctx context.Context, authenticatedUserID string, input ProfileUpdate) (*Profile, error) {
	if input.DisplayName == nil && input.Mobile == nil {
		return nil, invalid("profile", "at least one editable field is required")
	}
	if input.DisplayName != nil && !validText(*input.DisplayName, 100) {
		return nil, invalid("display_name", "must be valid text of at most 100 characters without control characters")
	}
	if input.Mobile != nil && !validText(*input.Mobile, 32) {
		return nil, invalid("mobile_number", "must be valid text of at most 32 characters without control characters")
	}
	if _, err := s.activeAccount(ctx, authenticatedUserID); err != nil {
		return nil, err
	}
	u, err := s.users.UpdateProfile(ctx, authenticatedUserID, repository.ProfilePatch{
		DisplayName: input.DisplayName, Mobile: input.Mobile,
	})
	if err != nil {
		return nil, err
	}
	return s.withBalance(ctx, u), nil
}

// DeleteAccount soft-deletes the active local account after explicit confirmation.
// Cross-service eligibility checks and session revocation are not yet integrated.
func (s *UserService) DeleteAccount(ctx context.Context, authenticatedUserID string, confirmed bool) error {
	if !confirmed {
		return invalid("confirmation", "explicit account deletion confirmation is required")
	}
	if _, err := s.activeAccount(ctx, authenticatedUserID); err != nil {
		return err
	}
	err := s.users.SoftDelete(ctx, authenticatedUserID)
	if err != nil {
		return err
	}

	return nil
}

// activeAccount looks up an authenticated account and rejects inactive accounts.
func (s *UserService) activeAccount(ctx context.Context, id string) (*repository.User, error) {
	if id == "" {
		return nil, invalid("identity", "an authenticated account identity is required")
	}
	if s.users == nil {
		return nil, ErrUnavailable
	}
	u, err := s.users.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if !u.Active {
		return nil, ErrInactive
	}
	return u, nil
}

// withBalance builds a profile and adds a credit balance when the lookup succeeds.
// A missing reader or failed lookup leaves CreditBalance nil.
func (s *UserService) withBalance(ctx context.Context, u *repository.User) *Profile {
	result := profile(u)
	if s.balances != nil {
		if balance, err := s.balances.Balance(ctx, u.ID); err == nil {
			result.CreditBalance = &balance
		}
	}
	return result
}

// profile converts a stored account to a profile without a credit balance.
func profile(u *repository.User) *Profile {
	return &Profile{ID: u.ID, Username: u.Username, Email: u.Email, Role: u.Role,
		Active: u.Active, DisplayName: u.DisplayName, Mobile: u.Mobile}
}

// nusDomain reports whether domain exactly matches the supported NUS student domain.
func nusDomain(domain string) bool {
	return domain == "u.nus.edu"
}

// validText reports whether value is valid UTF-8 with at most max runes
// and no control characters. Empty strings are valid.
func validText(value string, max int) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > max {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// invalid creates a validation error for the given field and message.
func invalid(field, message string) error {
	return &ValidationError{Field: field, Message: message}
}
