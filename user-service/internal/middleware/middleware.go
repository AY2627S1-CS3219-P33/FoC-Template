// Package middleware authenticates HTTP requests with Auth0 access tokens.
package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	jwtmiddleware "github.com/auth0/go-jwt-middleware/v3"
	"github.com/auth0/go-jwt-middleware/v3/jwks"
	"github.com/auth0/go-jwt-middleware/v3/validator"

	internalauth "user-service/internal/auth"
)

type CustomClaims struct{}

// Validate accepts custom claims without applying additional validation rules.
func (*CustomClaims) Validate(context.Context) error { return nil }

// Auth0 validates RS256 access tokens issued for this API.
type Auth0 struct {
	middleware *jwtmiddleware.JWTMiddleware
}

// NewAuth0 creates RS256 authentication middleware for the given tenant and audience.
func NewAuth0(domain, audience string) (*Auth0, error) {
	return newAuth0(domain, audience, nil)
}

// newAuth0 configures token validation and cached signing keys,
// optionally using a supplied HTTP client for key discovery.
func newAuth0(domain, audience string, httpClient *http.Client) (*Auth0, error) {
	issuer, err := internalauth.IssuerURL(domain)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(audience) == "" {
		return nil, errors.New("Auth0 audience is required")
	}
	providerOptions := []any{
		jwks.WithIssuerURL(issuer),
		jwks.WithStrictJWKSURIOrigin(),
	}
	if httpClient != nil {
		providerOptions = append(providerOptions, jwks.WithCustomClient(httpClient))
	}
	provider, err := jwks.NewCachingProvider(providerOptions...)
	if err != nil {
		return nil, errors.New("create Auth0 JWKS provider")
	}
	jwtValidator, err := validator.New(
		validator.WithKeyFunc(provider.KeyFunc),
		validator.WithAlgorithm(validator.RS256),
		validator.WithIssuer(issuer.String()),
		validator.WithAudience(strings.TrimSpace(audience)),
		validator.WithCustomClaims(func() *CustomClaims { return &CustomClaims{} }),
	)
	if err != nil {
		return nil, errors.New("create Auth0 JWT validator")
	}
	jwt, err := jwtmiddleware.New(
		jwtmiddleware.WithValidator(jwtValidator),
		jwtmiddleware.WithErrorHandler(authenticationError),
	)
	if err != nil {
		return nil, errors.New("create Auth0 JWT middleware")
	}
	return &Auth0{middleware: jwt}, nil
}

// Authentication validates the access token before invoking next
// and attaches the validated claims to the request context.
func (a *Auth0) Authentication(next http.Handler) http.Handler {
	return a.middleware.CheckJWT(next)
}

// Subject returns the trusted Auth0 sub claim attached by Authentication.
func Subject(ctx context.Context) (string, bool) {
	claims, err := jwtmiddleware.GetClaims[*validator.ValidatedClaims](ctx)
	if err != nil || claims.RegisteredClaims.Subject == "" {
		return "", false
	}
	return claims.RegisteredClaims.Subject, true
}

// BearerToken returns the token only after Authentication has validated it.
func BearerToken(r *http.Request) (string, bool) {
	scheme, token, ok := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
		return "", false
	}
	return strings.TrimSpace(token), true
}

// authenticationError writes an HTTP 401 response for a missing or invalid token.
func authenticationError(w http.ResponseWriter, _ *http.Request, err error) {
	if errors.Is(err, jwtmiddleware.ErrJWTMissing) {
		writeJSONError(w, http.StatusUnauthorized, "missing_token", "Authorization header with a bearer token is required.")
		return
	}
	writeJSONError(w, http.StatusUnauthorized, "invalid_token", "The access token is invalid or expired.")
}

// writeJSONError writes an error code and message as JSON with the supplied status.
func writeJSONError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "message": message})
}
