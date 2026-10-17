package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/apperror"
)

// Caller is the kind of token a route accepts.
type Caller int

const (
	// User routes accept login-flow tokens and reject machine tokens.
	User Caller = iota
	// Service routes accept client-credentials tokens and reject user tokens.
	Service
)

// Middleware authenticates bearer tokens and enforces permissions.
type Middleware struct {
	port Port
}

func NewMiddleware(port Port) *Middleware {
	if port == nil {
		panic("auth: port is required")
	}
	return &Middleware{port: port}
}

// Require authenticates the request, checks it came from the expected kind of
// caller and holds the permission, then stores the Principal in the context.
// Identity and permissions come only from the verified token.
func (m *Middleware) Require(caller Caller, permission Permission) func(http.Handler) http.Handler {
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

			if principal.Machine != (caller == Service) || !principal.Has(permission) {
				writeError(w, http.StatusForbidden, apperror.Forbidden, "insufficient permissions for this operation")
				return
			}

			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
		})
	}
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
	_ = json.NewEncoder(w).Encode(apperror.Error{Code: code, Message: message})
}
