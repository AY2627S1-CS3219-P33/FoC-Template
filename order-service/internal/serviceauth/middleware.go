package serviceauth

import (
	"net/http"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/auth"
)

// Middleware guards internal routes that other services call.
type Middleware struct {
	authn *auth.Middleware
}

// NewMiddleware builds internal-route middleware on the S0 verifier.
func NewMiddleware(authn *auth.Middleware) *Middleware {
	if authn == nil {
		panic("serviceauth: auth middleware is required")
	}
	return &Middleware{authn: authn}
}

// RequireService authenticates the bearer token and requires a service token
// carrying permission (NFR3.5.3). Authentication failures get the same
// responses as user routes (401 for missing, invalid or expired tokens). A
// user token gets 403 whatever permissions it carries, as does a service
// token without permission. On success the trusted Principal is put in the
// request context.
func (m *Middleware) RequireService(permission auth.Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := m.authn.Authenticate(w, r)
			if !ok {
				return
			}
			if !principal.IsService() {
				auth.WriteError(w, apperror.Forbidden, "a service credential is required for this operation")
				return
			}
			if !principal.Has(permission) {
				auth.WriteError(w, apperror.Forbidden, "insufficient permissions for this operation")
				return
			}
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), principal)))
		})
	}
}
