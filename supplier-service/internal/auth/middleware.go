// Package auth middleware enforces identity verification and permission policies
// for protected HTTP endpoints according to requirements F2.1-F2.4 and NFR3.3.
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
//  8. Insufficient permissions (RBAC policy violation):
//     - Authenticated principal lacks required permission (e.g. non-admin accessing ManageSuppliers).
//     - Returns 403 FORBIDDEN ("insufficient permissions for this operation").
//  9. Client identity or role tampering ignored:
//     - Request payload, query parameters, or client-supplied headers claiming roles/identities are ignored.
//     - Identity and roles are derived solely from the server-verified Port result.
// 10. Successful authentication & authorization:
//     - The trusted Principal is injected into the request context via WithPrincipal.
//     - The request proceeds to the downstream handler via next.ServeHTTP.
package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/apperror"
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

// RequirePermission returns an HTTP middleware adapter that authenticates the requester,
// verifies they possess the specified permission, and injects the trusted Principal into
// the request context.
func (m *Middleware) RequirePermission(permission Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, err := extractBearerToken(r.Header.Get("Authorization"))
			if err != nil {
				writeError(w, http.StatusUnauthorized, apperror.Unauthenticated, err.Error())
				return
			}

			principal, err := m.port.Authenticate(r.Context(), token)
			if err != nil {
				var authErr *AuthenticationError
				if errors.As(err, &authErr) {
					switch authErr.Kind {
					case InvalidCredential:
						writeError(w, http.StatusUnauthorized, apperror.Unauthenticated, "invalid or expired credentials")
					case AccountDisabled:
						writeError(w, http.StatusForbidden, apperror.Forbidden, "account is disabled")
					case VerifierUnavailable:
						writeError(w, http.StatusServiceUnavailable, apperror.DependencyUnavailable, "authentication service unavailable")
					default:
						writeError(w, http.StatusInternalServerError, apperror.Internal, "an unexpected internal error occurred")
					}
					return
				}
				writeError(w, http.StatusInternalServerError, apperror.Internal, "an unexpected internal error occurred")
				return
			}

			if !principal.Has(permission) {
				writeError(w, http.StatusForbidden, apperror.Forbidden, "insufficient permissions for this operation")
				return
			}

			ctx := WithPrincipal(r.Context(), principal)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireRead is a convenience middleware requiring the ReadSuppliers permission.
func (m *Middleware) RequireRead(next http.Handler) http.Handler {
	return m.RequirePermission(ReadSuppliers)(next)
}

// RequireManage is a convenience middleware requiring the ManageSuppliers permission.
func (m *Middleware) RequireManage(next http.Handler) http.Handler {
	return m.RequirePermission(ManageSuppliers)(next)
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

func writeError(w http.ResponseWriter, status int, code apperror.Code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(apperror.Error{
		Code:    code,
		Message: message,
	})
}
