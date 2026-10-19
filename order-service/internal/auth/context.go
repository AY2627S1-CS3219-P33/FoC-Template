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
