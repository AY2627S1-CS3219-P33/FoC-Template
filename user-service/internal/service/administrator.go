package service

import (
	"context"
	"errors"
	"strings"

	"user-service/internal/auth"
)

var ErrRoleManagementUnavailable = errors.New("Auth0 role management is unavailable")

type roleAPI interface {
	AssignRole(context.Context, string, string) error
	RemoveRole(context.Context, string, string) error
}

// AdministratorService changes only Auth0 roles; local account role values are
// not consulted or updated.
type AdministratorService struct {
	roles       roleAPI
	userRoleID  string
	adminRoleID string
}

func NewAdministratorService(roles roleAPI, userRoleID, adminRoleID string) *AdministratorService {
	return &AdministratorService{roles: roles, userRoleID: userRoleID, adminRoleID: adminRoleID}
}

// SetAdministrator grants or removes the Auth0 Administrator role.
func (s *AdministratorService) SetAdministrator(ctx context.Context, userID string, enabled bool) error {
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(userID) != userID || !strings.Contains(userID, "|") || strings.ContainsAny(userID, "/?#\r\n") {
		return invalid("user_id", "must be a valid Auth0 user ID")
	}
	if s.roles == nil || s.userRoleID == "" || s.adminRoleID == "" {
		return ErrRoleManagementUnavailable
	}
	if enabled {
		if err := s.roles.AssignRole(ctx, userID, s.adminRoleID); err != nil {
			return mapRoleError(err)
		}
		return nil
	}
	// Restore ordinary access before removing administrator permissions.
	if err := s.roles.AssignRole(ctx, userID, s.userRoleID); err != nil {
		return mapRoleError(err)
	}
	if err := s.roles.RemoveRole(ctx, userID, s.adminRoleID); err != nil {
		return mapRoleError(err)
	}
	return nil
}

func mapRoleError(err error) error {
	if errors.Is(err, auth.ErrManagementUserNotFound) {
		return ErrNotFound
	}
	return errors.Join(ErrRoleManagementUnavailable, err)
}
