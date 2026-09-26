package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"user-service/internal/middleware"
	"user-service/internal/service"
)

func registerUserRoutes(router *http.ServeMux, authentication *middleware.Auth0, provisioner Provisioner, userService *service.UserService) {
	router.Handle("GET /api/me", authentication.Authentication(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		getAccountInformation(w, req, provisioner, userService)
	})))
	router.Handle("PATCH /api/me", authentication.Authentication(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		updateAccountInformation(w, req, provisioner, userService)
	})))
}

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

	var input struct {
		DisplayName *string `json:"display_name"`
		Mobile      *string `json:"mobile_number"`
	}
	req.Body = http.MaxBytesReader(writer, req.Body, 4*1024)
	decoder := json.NewDecoder(req.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeProfileBodyError(writer, err)
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeProfileBodyError(writer, err)
		return
	}

	profile, err := userService.UpdateProfile(ctx, id, service.ProfileUpdate{
		DisplayName: input.DisplayName,
		Mobile:      input.Mobile,
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

func writeAccountError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrNotFound):
		writeError(writer, http.StatusForbidden, "account_not_provisioned", "A local account is required.")
	case errors.Is(err, service.ErrInactive):
		writeError(writer, http.StatusForbidden, "account_inactive", "This account is inactive.")
	default:
		writeError(writer, http.StatusServiceUnavailable, "account_unavailable", "Account information is unavailable.")
	}
}

func writeProfileBodyError(writer http.ResponseWriter, err error) {
	var sizeErr *http.MaxBytesError
	if errors.As(err, &sizeErr) {
		writeError(writer, http.StatusRequestEntityTooLarge, "body_too_large", "Request body must not exceed 4096 bytes.")
		return
	}
	writeError(writer, http.StatusBadRequest, "invalid_request", "Provide one JSON object containing only supported profile fields.")
}
