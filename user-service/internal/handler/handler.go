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
}

type Provisioner interface {
	Provision(context.Context, string, string) (*service.Profile, bool, error)
}

// Handler owns public assets and authenticated API routes.
type Handler struct {
	Router *http.ServeMux
}

func New(authConfig AuthConfig, authentication *middleware.Auth0, provisioner Provisioner) *Handler {
	router := http.NewServeMux()
	router.HandleFunc("GET /", serveIndex)
	router.HandleFunc("GET /assets/app.js", serveAsset("web/app.js", "application/javascript; charset=utf-8"))
	router.HandleFunc("GET /assets/styles.css", serveAsset("web/styles.css", "text/css; charset=utf-8"))
	router.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	router.HandleFunc("GET /api/auth/config", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, authConfig)
	})
	router.Handle("GET /api/private", authentication.Authentication(http.HandlerFunc(private)))
	router.Handle("POST /api/auth/provision", authentication.Authentication(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provision(w, r, provisioner)
	})))
	return &Handler{Router: router}
}

func serveIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	setPageSecurityHeaders(w)
	serveEmbedded(w, "web/index.html", "text/html; charset=utf-8")
}

func serveAsset(name, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { serveEmbedded(w, name, contentType) }
}

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

func setPageSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' https://cdn.auth0.com; style-src 'self'; img-src 'self' data: https:; connect-src 'self' https:; frame-src https:; base-uri 'none'; form-action 'self' https:; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

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
		writeError(w, http.StatusConflict, "account_conflict", "This email already belongs to another local account; automatic linking is disabled.")
	case errors.Is(err, service.ErrUnavailable):
		writeError(w, http.StatusServiceUnavailable, "provisioning_unavailable", "Account provisioning is unavailable.")
	default:
		writeError(w, http.StatusBadGateway, "auth0_profile_unavailable", "The Auth0 user profile could not be retrieved.")
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"error": code, "message": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
