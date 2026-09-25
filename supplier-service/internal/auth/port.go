package auth

import "context"

type Role string

const (
	RoleUser          Role = "user"
	RoleAdministrator Role = "administrator"
)

type Permission string

const (
	ReadSuppliers   Permission = "suppliers:read"
	ManageSuppliers Permission = "suppliers:manage"
)

// Principal is trusted authentication output. Supplier use cases accept this
// value and never accept account IDs or roles from request payloads.
type Principal struct {
	Subject string
	Roles   []Role
}

// FailureKind is a stable authentication outcome that middleware can map to
// the frozen HTTP contract without depending on a verifier implementation.
type FailureKind string

const (
	InvalidCredential   FailureKind = "invalid_credential"
	AccountDisabled     FailureKind = "account_disabled"
	VerifierUnavailable FailureKind = "verifier_unavailable"
)

// AuthenticationError deliberately excludes credential contents and provider
// error text so it is safe to return across the authentication boundary.
type AuthenticationError struct {
	Kind FailureKind
}

func (e *AuthenticationError) Error() string {
	return "authentication failed"
}

func (p Principal) Has(permission Permission) bool {
	if p.Subject == "" {
		return false
	}
	if permission == ReadSuppliers {
		return true
	}
	if permission == ManageSuppliers {
		for _, role := range p.Roles {
			if role == RoleAdministrator {
				return true
			}
		}
	}
	return false
}

// Port verifies opaque credentials and returns trusted identity data. It
// returns InvalidCredential for missing, invalid, or expired credentials,
// AccountDisabled when authoritative account state denies access, and
// VerifierUnavailable when identity cannot be established because its
// dependency is unavailable. A future JWT validator, session service, or
// user-service client can implement it without changing supplier use cases.
type Port interface {
	Authenticate(context.Context, string) (Principal, error)
}
