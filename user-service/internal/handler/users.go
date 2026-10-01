package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"user-service/internal/middleware"
	"user-service/internal/service"
)

// registerUserRoutes registers authenticated profile and account deletion endpoints.
func registerUserRoutes(router *http.ServeMux, authentication *middleware.Auth0, provisioner Provisioner, userService *service.UserService) {
	router.Handle("GET /api/me", authentication.Authentication(middleware.RequirePermission("users:read:self", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		getAccountInformation(w, req, provisioner, userService)
	}))))
	router.Handle("PATCH /api/me", authentication.Authentication(middleware.RequirePermission("users:update:self", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		updateAccountInformation(w, req, provisioner, userService)
	}))))
	router.Handle("DELETE /api/me", authentication.Authentication(middleware.RequirePermission("users:delete:self", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		deleteAccount(w, req, provisioner, userService)
	}))))
}

// getAccountInformation returns the profile for the authenticated active account.
func getAccountInformation(writer http.ResponseWriter, req *http.Request, pro Provisioner, userService *service.UserService) {
	ctx := req.Context()
	subject, ok := middleware.Subject(ctx)
	if !ok {
		writeError(writer, http.StatusUnauthorized, "authentication_required", "A valid access token is required.")
		return
	}

	id, err := pro.RequireActiveAccount(ctx, subject)
	if err != nil {
		writeAccountError(writer, err)
		return
	}

	profile, err := userService.GetProfile(ctx, id)
	if err != nil {
		writeAccountError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, profile)
}

// updateAccountInformation decodes a profile patch and updates the authenticated account.
// It rejects unknown fields, multiple JSON values, and bodies larger than 4096 bytes.
func updateAccountInformation(writer http.ResponseWriter, req *http.Request, pro Provisioner, userService *service.UserService) {
	ctx := req.Context()
	subject, ok := middleware.Subject(ctx)
	if !ok {
		writeError(writer, http.StatusUnauthorized, "authentication_required", "A valid access token is required.")
		return
	}

	id, err := pro.RequireActiveAccount(ctx, subject)
	if err != nil {
		writeAccountError(writer, err)
		return
	}

	var update_profile_input struct {
		DisplayName *string `json:"display_name"`
		Mobile      *string `json:"mobile_number"`
	}
	req.Body = http.MaxBytesReader(writer, req.Body, 4*1024)
	decoder := json.NewDecoder(req.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&update_profile_input); err != nil {
		writeProfileBodyError(writer, err)
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeProfileBodyError(writer, err)
		return
	}

	profile, err := userService.UpdateProfile(ctx, id, service.ProfileUpdate{
		DisplayName: update_profile_input.DisplayName,
		Mobile:      update_profile_input.Mobile,
	})
	if err != nil {
		if errors.Is(err, service.ErrValidation) {
			writeError(writer, http.StatusBadRequest, "invalid_profile", err.Error())
		} else {
			writeAccountError(writer, err)
		}
		return
	}
	writeJSON(writer, http.StatusOK, profile)
}

// deleteAccount handles deletion confirmation for the authenticated account.
func deleteAccount(writer http.ResponseWriter, req *http.Request, pro Provisioner, userService *service.UserService) {
	ctx := req.Context()
	subject, ok := middleware.Subject(ctx)
	if !ok {
		writeError(writer, http.StatusUnauthorized, "authentication_required", "A valid access token is required.")
		return
	}

	id, err := pro.RequireActiveAccount(ctx, subject)
	if err != nil {
		writeAccountError(writer, err)
		return
	}

	var delete_profile_input struct {
		DeleteConfirmation *bool `json:"delete_confirmation"`
	}
	req.Body = http.MaxBytesReader(writer, req.Body, 4*1024)
	decoder := json.NewDecoder(req.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&delete_profile_input); err != nil {
		writeProfileBodyError(writer, err)
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeProfileBodyError(writer, err)
		return
	}

	confirmation := delete_profile_input.DeleteConfirmation
	err = userService.DeleteAccount(ctx, id, confirmation != nil && *confirmation)
	if err != nil {
		writeAccountError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, nil)
}

// writeAccountError maps account lookup and service errors to JSON HTTP responses.
func writeAccountError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrNotFound):
		writeError(writer, http.StatusForbidden, "account_not_provisioned", "A local account is required.")
	case errors.Is(err, service.ErrInactive):
		writeError(writer, http.StatusForbidden, "account_inactive", "This account is inactive.")
	case errors.Is(err, service.ErrValidation):
		writeError(writer, http.StatusBadRequest, "bad_request", "The request contains missing or invalid fields.")
	default:
		writeError(writer, http.StatusServiceUnavailable, "account_unavailable", "Account information is unavailable.")
	}
}

// writeProfileBodyError reports oversized bodies as HTTP 413 and other body errors as HTTP 400.
func writeProfileBodyError(writer http.ResponseWriter, err error) {
	var sizeErr *http.MaxBytesError
	if errors.As(err, &sizeErr) {
		writeError(writer, http.StatusRequestEntityTooLarge, "body_too_large", "Request body must not exceed 4096 bytes.")
		return
	}
	writeError(writer, http.StatusBadRequest, "invalid_request", "Provide one JSON object containing only supported fields.")
}
