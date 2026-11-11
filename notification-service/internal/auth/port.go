// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: Implementation of notification-service Auth0 authentication and authorization.
// Author review: I validated correctness, edited for style, and added boundary checks.

package auth

import "context"

type Permission string

const (
	// ReadOwnNotifications lets a user read and count the notifications that
	// belong to their own Auth0 subject. Granted to users only.
	ReadOwnNotifications Permission = "notifications:read:own"
	// WriteOwnNotifications lets a user change the read state of their own
	// notifications. Granted to users only.
	WriteOwnNotifications Permission = "notifications:write:own"
)

var known = map[Permission]struct{}{
	ReadOwnNotifications: {}, WriteOwnNotifications: {},
}

// Principal is trusted authentication output. Use cases accept this value and
// never take identities or permissions from request data.
type Principal struct {
	// Subject is the Auth0 sub: a person (auth0|...) or a client (...@clients).
	Subject string
	// Machine is true for client-credentials tokens (another service).
	Machine     bool
	Permissions []Permission
}

func (p Principal) Has(permission Permission) bool {
	if p.Subject == "" {
		return false
	}
	if _, ok := known[permission]; !ok {
		return false
	}
	for _, granted := range p.Permissions {
		if granted == permission {
			return true
		}
	}
	return false
}

// FailureKind is a stable authentication outcome that middleware maps to HTTP.
type FailureKind string

const (
	InvalidCredential   FailureKind = "invalid_credential"
	VerifierUnavailable FailureKind = "verifier_unavailable"
)

// AuthenticationError deliberately excludes credential contents and provider
// error text so it is safe to return across the authentication boundary.
type AuthenticationError struct {
	Kind FailureKind
}

func (e *AuthenticationError) Error() string { return "authentication failed" }

// Port verifies opaque credentials and returns trusted identity data.
type Port interface {
	Authenticate(context.Context, string) (Principal, error)
}
