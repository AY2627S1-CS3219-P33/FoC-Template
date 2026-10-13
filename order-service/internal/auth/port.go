package auth

import "context"

type Role string

const (
	RoleUser          Role = "user"
	RoleAdministrator Role = "administrator"
)

type Permission string

const (
	// UseErrands lets a user request, accept and deliver errands (F1.6.1).
	UseErrands Permission = "errands:use"
	// ReviewErrands lets an administrator list and resolve errand reviews,
	// read the event log and change settings (F1.6.2).
	ReviewErrands Permission = "errands:review"
	// InternalAccess lets another service call order-service's internal
	// routes (supplier fence, account-deletion check). It is only honoured
	// on service tokens (NFR3.5.3).
	InternalAccess Permission = "orders:internal"
)

// TokenKind says whether a credential was issued to a person or to a service.
type TokenKind string

const (
	// UserToken is any access token not issued by the client-credentials grant.
	UserToken TokenKind = "user"
	// ServiceToken is an Auth0 machine-to-machine token, recognised by the
	// gty claim "client-credentials". Its Subject is "<client-id>@clients".
	ServiceToken TokenKind = "service"
)

// Principal is trusted authentication output. Errand use cases accept this
// value and never accept account IDs or roles from request payloads.
type Principal struct {
	// Subject is Auth0 sub within the configured issuer, not a local account ID.
	Subject string
	// Kind is derived from the verified token, never from request data.
	// The zero value is treated as neither kind and grants nothing.
	Kind TokenKind
	// Roles is retained for compatibility and never grants access.
	Roles       []Role
	Permissions []Permission
}

// IsService reports whether the principal authenticated with a service token.
func (p Principal) IsService() bool { return p.Kind == ServiceToken }

// IsUser reports whether the principal authenticated with a user token.
func (p Principal) IsUser() bool { return p.Kind == UserToken }

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

// Has reports whether the token explicitly grants a known permission. It does
// not check the token kind; callers that need a user or a service also check
// IsUser or IsService (RequirePermission does this for user routes).
func (p Principal) Has(permission Permission) bool {
	if p.Subject == "" || !knownPermission(permission) {
		return false
	}
	for _, granted := range p.Permissions {
		if granted == permission {
			return true
		}
	}
	return false
}

func knownPermission(permission Permission) bool {
	switch permission {
	case UseErrands, ReviewErrands, InternalAccess:
		return true
	default:
		return false
	}
}

// Port verifies opaque credentials and returns trusted identity data. It
// returns InvalidCredential for missing, invalid, or expired credentials,
// AccountDisabled when authoritative account state denies access, and
// VerifierUnavailable when identity cannot be established because its
// dependency is unavailable. A future JWT validator, session service, or
// user-service client can implement it without changing errand use cases.
type Port interface {
	Authenticate(context.Context, string) (Principal, error)
}
