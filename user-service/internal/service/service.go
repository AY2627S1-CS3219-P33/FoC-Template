// Package service implements internal student-account business operations.
package service

import (
	"context"
	"errors"
	"fmt"
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

func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }
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

func NewUserService(users repository.UserRepository, balances CreditBalanceReader) *UserService {
	return &UserService{users: users, balances: balances}
}

func (s *UserService) GetProfile(ctx context.Context, authenticatedUserID string) (*Profile, error) {
	u, err := s.activeAccount(ctx, authenticatedUserID)
	if err != nil {
		return nil, err
	}
	return s.withBalance(ctx, u), nil
}

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

// DeleteAccount deliberately never calls SoftDelete in this slice. A future
// implementation must coordinate cross-service eligibility and invalidate all
// sessions before enabling deletion; checking a remote snapshot is insufficient.
func (s *UserService) DeleteAccount(ctx context.Context, authenticatedUserID string, confirmed bool) error {
	if !confirmed {
		return invalid("confirmation", "explicit account deletion confirmation is required")
	}
	if _, err := s.activeAccount(ctx, authenticatedUserID); err != nil {
		return err
	}
	return fmt.Errorf("account deletion requires coordinated eligibility checks and session revocation: %w", ErrUnavailable)
}

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

func (s *UserService) withBalance(ctx context.Context, u *repository.User) *Profile {
	result := profile(u)
	if s.balances != nil {
		if balance, err := s.balances.Balance(ctx, u.ID); err == nil {
			result.CreditBalance = &balance
		}
	}
	return result
}

func profile(u *repository.User) *Profile {
	return &Profile{ID: u.ID, Username: u.Username, Email: u.Email, Role: u.Role,
		Active: u.Active, DisplayName: u.DisplayName, Mobile: u.Mobile}
}

func nusDomain(domain string) bool {
	return domain == "u.nus.edu"
}

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

func invalid(field, message string) error {
	return &ValidationError{Field: field, Message: message}
}
