// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: Implementation of notification-service Auth0 authentication and authorization.
// Author review: I validated correctness, edited for style, and added boundary checks.

package auth

import "context"

type principalContextKey struct{}

// WithPrincipal returns a context carrying trusted authentication output.
func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, principal)
}

// PrincipalFromContext retrieves the principal stored by WithPrincipal.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	return principal, ok
}
