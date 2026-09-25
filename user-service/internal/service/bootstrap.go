package service

import (
	"context"
	"errors"
	"net/mail"
	"strings"
	"unicode"

	"user-service/internal/repository"
)

var ErrBootstrapRecovery = errors.New("bootstrap completed but no super administrator exists; explicit recovery required")

// BootstrapSuperAdmin links a deployment-supplied Auth0 identity to the initial
// local administrator. It never creates an Auth0 identity or promotes a user.
func BootstrapSuperAdmin(ctx context.Context, repo repository.BootstrapRepository, input repository.CreateAuth0User) error {
	return repo.WithBootstrapLock(ctx, func(tx repository.BootstrapTransaction) error {
		completed, exists, err := tx.State(ctx)
		if err != nil {
			return err
		}
		if completed {
			if !exists {
				return ErrBootstrapRecovery
			}
			return nil
		}
		if exists {
			return tx.MarkCompleted(ctx)
		}
		input.Subject = strings.TrimSpace(input.Subject)
		input.Username = strings.TrimSpace(input.Username)
		input.Email = strings.ToLower(strings.TrimSpace(input.Email))
		provider, id, ok := strings.Cut(input.Subject, "|")
		if !ok || provider == "" || id == "" || !validText(input.Subject, 255) || strings.ContainsFunc(input.Subject, unicode.IsSpace) {
			return invalid("BOOTSTRAP_AUTH0_SUBJECT", "must be an existing Auth0 identity in provider|id format")
		}
		if input.Username == "" || !validText(input.Username, 64) {
			return invalid("BOOTSTRAP_USERNAME", "must contain 1 to 64 characters without control characters")
		}
		address, err := mail.ParseAddress(input.Email)
		if err != nil || address.Address != input.Email || len(input.Email) > 254 || !validText(input.Email, 254) {
			return invalid("BOOTSTRAP_EMAIL", "must be a valid email address")
		}
		if err := tx.CreateSuperAdmin(ctx, input); err != nil {
			return err
		}
		return tx.MarkCompleted(ctx)
	})
}
