// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-09-29
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

package auth

import "context"

type principalContextKey struct{}

// WithPrincipal returns a context carrying trusted authentication output.
// Transport handlers use this helper instead of accepting identity or roles
// from request data.
func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, principal)
}

// PrincipalFromContext retrieves trusted authentication output previously
// stored by WithPrincipal.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	return principal, ok
}
