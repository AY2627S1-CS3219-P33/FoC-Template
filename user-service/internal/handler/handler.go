// Package handler adapts HTTP requests to user-service operations.
package handler

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"net/http"

	"user-service/internal/middleware"
	"user-service/internal/repository"
	"user-service/internal/service"
)

//go:embed web/*
var webAssets embed.FS

type AuthConfig struct {
	Domain   string `json:"domain"`
	ClientID string `json:"clientId"`
	Audience string `json:"audience"`
	// Dev signals the browser page to use the local mock sign-in flow instead of
	// the Auth0 SPA. DEV-ONLY: set true only when the mock issuer is active.
	Dev bool `json:"dev,omitempty"`
}

type Provisioner interface {
	Provision(context.Context, string, string) (*service.Profile, bool, error)
	RequireActiveAccount(context.Context, string) (string, error)
}

// Handler owns public assets and authenticated API routes.
type Handler struct {
	Router *http.ServeMux
}

// New creates a handler with public web assets and authenticated API routes.
func New(authConfig AuthConfig, authentication *middleware.Auth0, provisioner Provisioner, userService *service.UserService) *Handler {
	router := http.NewServeMux()
	registerUserRoutes(router, authentication, provisioner, userService)
	router.HandleFunc("GET /", serveIndex)
	router.HandleFunc("GET /assets/app.js", serveAsset("web/app.js", "application/javascript; charset=utf-8"))
	router.HandleFunc("GET /assets/styles.css", serveAsset("web/styles.css", "text/css; charset=utf-8"))
	router.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	router.HandleFunc("GET /api/auth/config", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, authConfig)
	})
	router.Handle("GET /api/private", authentication.Authentication(requireActiveAccount(provisioner, http.HandlerFunc(private))))
	router.Handle("POST /api/auth/logout", authentication.Authentication(http.HandlerFunc(logout)))
	router.Handle("POST /api/auth/provision", authentication.Authentication(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provision(w, r, provisioner)
	})))
	return &Handler{Router: router}
}

// requireActiveAccount allows requests only when the authenticated subject
// is linked to an active local account.
func requireActiveAccount(authorizer Provisioner, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authorizer == nil {
			writeError(w, http.StatusServiceUnavailable, "account_check_unavailable", "Local account status could not be verified.")
			return
		}
		subject, ok := middleware.Subject(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "authentication_required", "A valid access token is required.")
			return
		}
		_, err := authorizer.RequireActiveAccount(r.Context(), subject)
		switch {
		case errors.Is(err, service.ErrNotFound):
			writeError(w, http.StatusForbidden, "account_not_provisioned", "Provision a local account before accessing this resource.")
		case errors.Is(err, service.ErrInactive):
			writeError(w, http.StatusForbidden, "account_inactive", "This local account is inactive.")
		case err != nil:
			writeError(w, http.StatusServiceUnavailable, "account_check_unavailable", "Local account status could not be verified.")
		default:
			next.ServeHTTP(w, r)
		}
	})
}

// serveIndex serves the application page at the root path with security headers.
func serveIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	setPageSecurityHeaders(w)
	serveEmbedded(w, "web/index.html", "text/html; charset=utf-8")
}

// serveAsset returns a handler for an embedded asset with the given content type.
func serveAsset(name, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { serveEmbedded(w, name, contentType) }
}

// serveEmbedded writes an embedded asset and disables response caching.
func serveEmbedded(w http.ResponseWriter, name, contentType string) {
	contents, err := webAssets.ReadFile(name)
	if err != nil {
		http.Error(w, "asset unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(contents)
}

// setPageSecurityHeaders sets the application page's content, referrer, and MIME policies.
func setPageSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' https://cdn.auth0.com; style-src 'self'; img-src 'self' data: https:; connect-src 'self' https:; frame-src https:; base-uri 'none'; form-action 'self' https:; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

// private returns the authenticated subject for the protected example endpoint.
func private(w http.ResponseWriter, r *http.Request) {
	subject, ok := middleware.Subject(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication_required", "A valid access token is required.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"message": "Authenticated request succeeded.", "subject": subject,
	})
}

// logout acknowledges an authenticated logout request.
// It does not revoke access tokens or terminate Auth0 sessions.
func logout(w http.ResponseWriter, r *http.Request) {
	if _, ok := middleware.Subject(r.Context()); !ok {
		writeError(w, http.StatusUnauthorized, "authentication_required", "A valid access token is required.")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

// provision resolves or creates a local account using the authenticated Auth0 identity.
// It returns HTTP 201 when an account is created and HTTP 200 for an existing account.
func provision(w http.ResponseWriter, r *http.Request, provisioner Provisioner) {
	if provisioner == nil {
		writeError(w, http.StatusServiceUnavailable, "provisioning_unavailable", "Account provisioning is unavailable.")
		return
	}
	subject, ok := middleware.Subject(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication_required", "A valid access token is required.")
		return
	}
	token, ok := middleware.BearerToken(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication_required", "A valid access token is required.")
		return
	}
	profile, created, err := provisioner.Provision(r.Context(), subject, token)
	if err != nil {
		writeProvisionError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, profile)
}

// writeProvisionError maps provisioning failures to public JSON HTTP responses.
func writeProvisionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrIdentityUnverified):
		writeError(w, http.StatusForbidden, "email_not_verified", "Verify your Auth0 email before signing in.")
	case errors.Is(err, service.ErrIdentityIneligible):
		writeError(w, http.StatusForbidden, "ineligible_email", "A verified NUS email address is required.")
	case errors.Is(err, service.ErrIdentityMismatch):
		writeError(w, http.StatusUnauthorized, "identity_mismatch", "The Auth0 profile does not match the access token.")
	case errors.Is(err, service.ErrInactive), errors.Is(err, repository.ErrInactive):
		writeError(w, http.StatusForbidden, "account_inactive", "This local account is inactive.")
	case errors.Is(err, repository.ErrConflict):
		writeError(w, http.StatusConflict, "account_conflict", "This username, email, or Auth0 identity already belongs to another local account; automatic linking is disabled.")
	case errors.Is(err, service.ErrProfileUnavailable):
		writeError(w, http.StatusBadGateway, "auth0_profile_unavailable", "The Auth0 user profile could not be retrieved.")
	case errors.Is(err, service.ErrUnavailable):
		writeError(w, http.StatusServiceUnavailable, "provisioning_unavailable", "Account provisioning is unavailable.")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "An unexpected error occurred.")
	}
}

// writeError writes a JSON error code and message with the supplied HTTP status.
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"error": code, "message": message})
}

// writeJSON encodes value as JSON with the supplied status and disables caching.
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
