package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/mail"
	"strings"

	"user-service/internal/auth"
	"user-service/internal/repository"
)

var (
	ErrIdentityUnverified = errors.New("Auth0 email is not verified")
	ErrIdentityIneligible = errors.New("Auth0 email is not an eligible NUS address")
	ErrIdentityMismatch   = errors.New("Auth0 profile does not match the access token")
	ErrProfileUnavailable = errors.New("Auth0 profile is unavailable")
)

// Auth0Provisioner creates local accounts from trusted Auth0 identity data.
type Auth0Provisioner struct {
	users    repository.Auth0UserRepository
	profiles auth.UserInfoReader
	wallets  WalletCreator
}

// WalletCreator creates, or confirms, the credit wallet for an account. It
// must be idempotent per owner.
type WalletCreator interface {
	EnsureWallet(ctx context.Context, ownerID, auth0Subject string) error
}

// WithWalletCreator makes Provision ensure a wallet for every active account.
// Failures are logged and never fail sign-in; the next Provision retries.
func (s *Auth0Provisioner) WithWalletCreator(wallets WalletCreator) *Auth0Provisioner {
	s.wallets = wallets
	return s
}

func (s *Auth0Provisioner) ensureWallet(ctx context.Context, u *repository.User, subject string) {
	if s.wallets == nil {
		return
	}
	if err := s.wallets.EnsureWallet(ctx, u.ID, subject); err != nil {
		log.Printf("ensure wallet for account %s: %v", u.ID, err)
	}
}

// NewAuth0Provisioner creates a provisioner backed by local accounts and Auth0 profiles.
func NewAuth0Provisioner(users repository.Auth0UserRepository, profiles auth.UserInfoReader) *Auth0Provisioner {
	return &Auth0Provisioner{users: users, profiles: profiles}
}

// RequireActiveAccount returns the local account ID for an Auth0 subject.
// It rejects missing and inactive accounts without provisioning a new account.
func (s *Auth0Provisioner) RequireActiveAccount(ctx context.Context, subject string) (string, error) {
	if s.users == nil {
		return "", ErrUnavailable
	}
	u, err := s.users.FindByAuth0Subject(ctx, subject)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if !u.Active {
		return "", ErrInactive
	}
	return u.ID, nil
}

// Provision returns the local profile and whether this call created it.
func (s *Auth0Provisioner) Provision(ctx context.Context, subject, accessToken string) (*Profile, bool, error) {
	if strings.TrimSpace(subject) == "" || strings.TrimSpace(accessToken) == "" {
		return nil, false, ErrIdentityMismatch
	}
	if s.users == nil || s.profiles == nil {
		return nil, false, ErrUnavailable
	}
	u, err := s.users.FindByAuth0Subject(ctx, subject)
	if err == nil {
		if !u.Active {
			return nil, false, ErrInactive
		}
		s.ensureWallet(ctx, u, subject)
		return profile(u), false, nil
	}
	if !errors.Is(err, repository.ErrNotFound) {
		return nil, false, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}

	identity, err := s.profiles.Get(ctx, accessToken)
	if err != nil {
		return nil, false, fmt.Errorf("%w: %v", ErrProfileUnavailable, err)
	}
	if identity.Subject != subject {
		return nil, false, ErrIdentityMismatch
	}
	if !identity.EmailVerified {
		return nil, false, ErrIdentityUnverified
	}
	email := strings.ToLower(strings.TrimSpace(identity.Email))
	if !validNUSEmail(email) {
		return nil, false, ErrIdentityIneligible
	}
	username := strings.TrimSpace(identity.Nickname)
	if username == "" || !validText(username, 64) {
		username, _, _ = strings.Cut(email, "@")
		username = truncateRunes(username, 64)
	}
	displayName := strings.TrimSpace(identity.Name)
	if !validText(displayName, 100) {
		displayName = ""
	}
	u, created, err := s.users.CreateAuth0(ctx, repository.CreateAuth0User{
		Subject: subject, Username: username, Email: email, DisplayName: displayName,
	})
	if err != nil {
		if errors.Is(err, repository.ErrConflict) {
			return nil, false, err
		}
		return nil, false, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	s.ensureWallet(ctx, u, subject)
	return profile(u), created, nil
}

// validNUSEmail reports whether email is a bare address of at most 254 bytes
// in the supported NUS student domain. The caller must normalize its case.
func validNUSEmail(email string) bool {
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email || len(email) > 254 {
		return false
	}
	local, domain, ok := strings.Cut(email, "@")
	return ok && local != "" && !strings.Contains(domain, "@") && nusDomain(domain)
}

// truncateRunes limits value to max Unicode code points without splitting UTF-8 sequences.
func truncateRunes(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}
