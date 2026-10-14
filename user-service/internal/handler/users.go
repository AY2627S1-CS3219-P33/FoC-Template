package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"user-service/internal/middleware"
	"user-service/internal/service"
)

// registerUserRoutes registers authenticated profile and account deletion endpoints.
func registerUserRoutes(router *http.ServeMux, authentication *middleware.Auth0, provisioner Provisioner, userService *service.UserService, administratorService *service.AdministratorService, adminUsersService *service.AdminUsersService) {
	router.Handle("GET /api/me", authentication.Authentication(middleware.RequirePermission("users:read:self", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		getAccountInformation(w, req, provisioner, userService)
	}))))
	router.Handle("PATCH /api/me", authentication.Authentication(middleware.RequirePermission("users:update:self", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		updateAccountInformation(w, req, provisioner, userService)
	}))))
	router.Handle("DELETE /api/me", authentication.Authentication(middleware.RequirePermission("users:delete:self", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		deleteAccount(w, req, provisioner, userService)
	}))))
	router.Handle("PUT /api/admin/users/{userID}/administrator", authentication.Authentication(middleware.RequirePermission("users:manage:roles", requireActiveAccount(provisioner, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		setAdministrator(w, req, administratorService, true)
	})))))
	router.Handle("GET /api/admin/users", authentication.Authentication(middleware.RequirePermission("users:manage:roles", requireActiveAccount(provisioner, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		listAdminUsers(w, req, adminUsersService)
	})))))
	router.Handle("DELETE /api/admin/users/{userID}/administrator", authentication.Authentication(middleware.RequirePermission("users:manage:roles", requireActiveAccount(provisioner, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		setAdministrator(w, req, administratorService, false)
	})))))
}

func listAdminUsers(writer http.ResponseWriter, req *http.Request, adminUsersService *service.AdminUsersService) {
	query := req.URL.Query()
	page, err := optionalQueryInt(query["page"], 0)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_query", "page must be a non-negative integer.")
		return
	}
	perPage, err := optionalQueryInt(query["per_page"], 0)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_query", "per_page must be an integer from 1 to 100.")
		return
	}
	emailValues := query["email"]
	if len(emailValues) > 1 {
		writeError(writer, http.StatusBadRequest, "invalid_query", "Provide at most one email value.")
		return
	}
	roleValues := query["role"]
	if len(roleValues) > 1 {
		writeError(writer, http.StatusBadRequest, "invalid_query", "Provide at most one role value.")
		return
	}
	cursorValues := query["cursor"]
	if len(cursorValues) > 1 {
		writeError(writer, http.StatusBadRequest, "invalid_query", "Provide at most one cursor value.")
		return
	}
	email := ""
	if len(emailValues) == 1 {
		email = emailValues[0]
	}
	role, cursor := "", ""
	if len(roleValues) == 1 {
		role = roleValues[0]
	}
	if len(cursorValues) == 1 {
		cursor = cursorValues[0]
	}
	if strings.TrimSpace(email) != email {
		writeError(writer, http.StatusBadRequest, "invalid_query", "email must not have surrounding whitespace.")
		return
	}
	if adminUsersService == nil {
		writeError(writer, http.StatusServiceUnavailable, "user_listing_unavailable", "The Auth0 user directory is unavailable.")
		return
	}
	users, err := adminUsersService.List(req.Context(), page, perPage, email, role, cursor)
	if err != nil {
		if errors.Is(err, service.ErrValidation) {
			writeError(writer, http.StatusBadRequest, "invalid_query", "page, per_page, or email is invalid.")
		} else {
			writeError(writer, http.StatusServiceUnavailable, "user_listing_unavailable", "The Auth0 user directory is unavailable.")
		}
		return
	}
	writeJSON(writer, http.StatusOK, users)
}

func optionalQueryInt(values []string, defaultValue int) (int, error) {
	if len(values) == 0 {
		return defaultValue, nil
	}
	if len(values) != 1 {
		return 0, errors.New("query parameter must appear once")
	}
	value, err := strconv.Atoi(values[0])
	if err != nil {
		return 0, err
	}
	return value, nil
}

func setAdministrator(writer http.ResponseWriter, req *http.Request, administratorService *service.AdministratorService, enabled bool) {
	if administratorService == nil {
		writeError(writer, http.StatusServiceUnavailable, "role_management_unavailable", "Administrator role management is unavailable.")
		return
	}
	if err := administratorService.SetAdministrator(req.Context(), req.PathValue("userID"), enabled); err != nil {
		switch {
		case errors.Is(err, service.ErrValidation):
			writeError(writer, http.StatusBadRequest, "invalid_user_id", "The Auth0 user ID is invalid.")
		case errors.Is(err, service.ErrNotFound):
			writeError(writer, http.StatusNotFound, "user_not_found", "The Auth0 user was not found.")
		default:
			writeError(writer, http.StatusServiceUnavailable, "role_management_unavailable", "Administrator role management is unavailable.")
		}
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusNoContent)
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
	setAuthorizationRole(profile, req)
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
	setAuthorizationRole(profile, req)
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
