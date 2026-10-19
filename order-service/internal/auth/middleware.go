// Package auth middleware enforces identity verification and permission policies
// for user-facing HTTP endpoints according to F1.6 and NFR3.3.
//
// Authentication and authorization cases handled:
//  1. Missing Authorization header:
//     - Missing header returns 401 UNAUTHENTICATED ("authorization header is required").
//  2. Malformed scheme:
//     - Header does not follow the "Bearer <token>" format (e.g., Basic auth or single word).
//     - Returns 401 UNAUTHENTICATED ("authorization header must use Bearer scheme").
//  3. Empty token:
//     - "Bearer " prefix is present but the token is blank or whitespace.
//     - Returns 401 UNAUTHENTICATED ("bearer token is empty").
//  4. Invalid or expired credentials:
//     - Port returns InvalidCredential.
//     - Returns 401 UNAUTHENTICATED ("invalid or expired credentials").
//  5. Account disabled:
//     - Port returns AccountDisabled for an authoritatively deactivated account.
//     - Returns 403 FORBIDDEN ("account is disabled").
//  6. Verifier dependency unavailable:
//     - Port returns VerifierUnavailable (identity provider / user service unreachable).
//     - Returns 503 DEPENDENCY_UNAVAILABLE ("authentication service unavailable").
//  7. Unexpected internal failure:
//     - Port returns an unclassified error.
//     - Returns 500 INTERNAL ("an unexpected internal error occurred") without leaking token or provider details.
//  8. Service credential on a user route:
//     - The token is not a user token (e.g. a client-credentials token), whatever permissions it carries.
//     - Returns 403 FORBIDDEN ("a user credential is required for this operation") (NFR3.5.3).
//  9. Insufficient permissions (RBAC policy violation):
//     - Authenticated principal lacks required permission (e.g. non-admin accessing ReviewErrands).
//     - Returns 403 FORBIDDEN ("insufficient permissions for this operation").
//
// 10. Client identity or role tampering ignored:
//   - Request payload, query parameters, or client-supplied headers claiming roles/identities are ignored.
//   - Identity and permissions are derived solely from the server-verified Port result.
//
// 11. Successful authentication & authorization:
//   - The trusted Principal is injected into the request context via WithPrincipal.
//   - The request proceeds to the downstream handler via next.ServeHTTP.
//
// Internal routes called by other services do not use RequirePermission; they
// require a service token with InternalAccess (internal/serviceauth).
package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
)

// Middleware enforces identity verification and permission policies for HTTP endpoints.
type Middleware struct {
	port Port
}

// NewMiddleware constructs an authentication middleware backed by the given Port.
func NewMiddleware(port Port) *Middleware {
	if port == nil {
		panic("auth: port is required")
	}
	return &Middleware{port: port}
}

// Authenticate verifies the bearer token and returns the trusted Principal.
// On failure it has already written the error response and returns false.
// It performs no permission or token-kind check.
func (m *Middleware) Authenticate(w http.ResponseWriter, r *http.Request) (Principal, bool) {
	token, err := extractBearerToken(r.Header.Get("Authorization"))
	if err != nil {
		WriteError(w, apperror.Unauthenticated, err.Error())
		return Principal{}, false
	}

	principal, err := m.port.Authenticate(r.Context(), token)
	if err != nil {
		var authErr *AuthenticationError
		if errors.As(err, &authErr) {
			switch authErr.Kind {
			case InvalidCredential:
				WriteError(w, apperror.Unauthenticated, "invalid or expired credentials")
			case AccountDisabled:
				WriteError(w, apperror.Forbidden, "account is disabled")
			case VerifierUnavailable:
				WriteError(w, apperror.DependencyUnavailable, "authentication service unavailable")
			default:
				WriteError(w, apperror.Internal, "an unexpected internal error occurred")
			}
			return Principal{}, false
		}
		WriteError(w, apperror.Internal, "an unexpected internal error occurred")
		return Principal{}, false
	}
	return principal, true
}

// RequirePermission returns an HTTP middleware adapter that authenticates the requester,
// verifies they hold a user token with the specified permission, and injects the trusted
// Principal into the request context.
func (m *Middleware) RequirePermission(permission Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := m.Authenticate(w, r)
			if !ok {
				return
			}

			if !principal.IsUser() {
				WriteError(w, apperror.Forbidden, "a user credential is required for this operation")
				return
			}

			if !principal.Has(permission) {
				WriteError(w, apperror.Forbidden, "insufficient permissions for this operation")
				return
			}

			ctx := WithPrincipal(r.Context(), principal)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireUse is a convenience middleware requiring the UseErrands permission.
func (m *Middleware) RequireUse(next http.Handler) http.Handler {
	return m.RequirePermission(UseErrands)(next)
}

// RequireReview is a convenience middleware requiring the ReviewErrands permission.
func (m *Middleware) RequireReview(next http.Handler) http.Handler {
	return m.RequirePermission(ReviewErrands)(next)
}

func extractBearerToken(header string) (string, error) {
	if header == "" {
		return "", errors.New("authorization header is required")
	}

	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", errors.New("authorization header must use Bearer scheme")
	}

	token := strings.TrimSpace(parts[1])
	if token == "" {
		return "", errors.New("bearer token is empty")
	}

	return token, nil
}

// WriteError writes the standard error body with the status from
// apperror.HTTPStatus. Internal-route middleware reuses it so every
// authentication failure has the same shape.
func WriteError(w http.ResponseWriter, code apperror.Code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(apperror.HTTPStatus(code))
	_ = json.NewEncoder(w).Encode(apperror.Error{
		Code:    code,
		Message: message,
	})
}
