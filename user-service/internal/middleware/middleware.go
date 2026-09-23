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

func (*CustomClaims) Validate(context.Context) error { return nil }

// Auth0 validates RS256 access tokens issued for this API.
type Auth0 struct {
	middleware *jwtmiddleware.JWTMiddleware
}

func NewAuth0(domain, audience string) (*Auth0, error) {
	issuer, err := internalauth.IssuerURL(domain)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(audience) == "" {
		return nil, errors.New("Auth0 audience is required")
	}
	provider, err := jwks.NewCachingProvider(
		jwks.WithIssuerURL(issuer),
		jwks.WithStrictJWKSURIOrigin(),
	)
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

func authenticationError(w http.ResponseWriter, _ *http.Request, err error) {
	if errors.Is(err, jwtmiddleware.ErrJWTMissing) {
		writeJSONError(w, http.StatusUnauthorized, "missing_token", "Authorization header with a bearer token is required.")
		return
	}
	writeJSONError(w, http.StatusUnauthorized, "invalid_token", "The access token is invalid or expired.")
}

func writeJSONError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "message": message})
}
