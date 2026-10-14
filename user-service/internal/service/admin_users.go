package service

import (
	"context"
	"errors"
	"strings"

	"user-service/internal/auth"
)

const (
	defaultAdminUserPageSize = 25
	maxAdminUserPageSize     = 100
	maxAuth0UserResults      = 1000
)

var ErrUserListingUnavailable = errors.New("Auth0 user listing is unavailable")

type userDirectoryAPI interface {
	ListUsers(context.Context, int, int, string) (auth.ManagedUsersPage, error)
	ListRoleUsers(context.Context, string, string, int) ([]auth.ManagedUser, string, error)
	UserRoles(context.Context, string) ([]auth.ManagedRole, error)
	AdministratorIDs(context.Context, string) (map[string]struct{}, error)
}

// AdminUsersService provides a paginated view of Auth0 users and role assignments.
type AdminUsersService struct {
	directory   userDirectoryAPI
	userRoleID  string
	adminRoleID string
}

func NewAdminUsersService(directory userDirectoryAPI, userRoleID, adminRoleID string) *AdminUsersService {
	return &AdminUsersService{directory: directory, userRoleID: userRoleID, adminRoleID: adminRoleID}
}

func (s *AdminUsersService) List(ctx context.Context, page, perPage int, email, role, cursor string) (auth.ManagedUsersPage, error) {
	if perPage == 0 {
		perPage = defaultAdminUserPageSize
	}
	if page < 0 || perPage < 1 || perPage > maxAdminUserPageSize || len(email) > 254 || strings.ContainsAny(email, "\r\n\x00") || len(cursor) > 512 || strings.ContainsAny(cursor, "\r\n\x00") || role != "" && role != "administrator" && role != "user" {
		return auth.ManagedUsersPage{}, invalid("query", "page, per_page, or email is invalid")
	}
	if s == nil || s.directory == nil {
		return auth.ManagedUsersPage{}, ErrUserListingUnavailable
	}
	if role == "" {
		if page > (maxAuth0UserResults-1)/perPage || cursor != "" {
			return auth.ManagedUsersPage{}, invalid("query", "page is out of range")
		}
		users, err := s.directory.ListUsers(ctx, page, perPage, email)
		if err != nil {
			return auth.ManagedUsersPage{}, errors.Join(ErrUserListingUnavailable, err)
		}
		return users, nil
	}
	if s.userRoleID == "" || s.adminRoleID == "" || s.userRoleID == s.adminRoleID {
		return auth.ManagedUsersPage{}, ErrUserListingUnavailable
	}
	if page != 0 && email == "" || email != "" && cursor != "" || email != "" && page > (maxAuth0UserResults-1)/perPage {
		return auth.ManagedUsersPage{}, invalid("query", "role requires configured role IDs and compatible pagination parameters")
	}
	var err error
	roleID := s.adminRoleID
	if role == "user" {
		roleID = s.userRoleID
	}
	var users auth.ManagedUsersPage
	if email != "" {
		users, err = s.directory.ListUsers(ctx, page, perPage, email)
		if err == nil {
			users.Users, err = s.filterEmailUsersByRole(ctx, users.Users, role)
			users.Total = nil
		}
	} else {
		var listed []auth.ManagedUser
		var next string
		listed, next, err = s.directory.ListRoleUsers(ctx, roleID, cursor, perPage)
		if err == nil && role == "user" {
			var administrators map[string]struct{}
			administrators, err = s.directory.AdministratorIDs(ctx, s.adminRoleID)
			if err == nil {
				filtered := listed[:0]
				for _, user := range listed {
					if _, isAdministrator := administrators[user.ID]; !isAdministrator {
						filtered = append(filtered, user)
					}
				}
				listed = filtered
			}
		}
		users = auth.ManagedUsersPage{Users: listed, Page: 0, PerPage: perPage, Role: role, Next: next}
	}
	if err != nil {
		return auth.ManagedUsersPage{}, errors.Join(ErrUserListingUnavailable, err)
	}
	users.Role = role
	return users, nil
}

func (s *AdminUsersService) filterEmailUsersByRole(ctx context.Context, users []auth.ManagedUser, role string) ([]auth.ManagedUser, error) {
	filtered := make([]auth.ManagedUser, 0, len(users))
	for _, user := range users {
		roles, err := s.directory.UserRoles(ctx, user.ID)
		if err != nil {
			return nil, err
		}
		isUser, isAdministrator := false, false
		for _, assigned := range roles {
			isUser = isUser || assigned.ID == s.userRoleID
			isAdministrator = isAdministrator || assigned.ID == s.adminRoleID
		}
		if role == "administrator" && isAdministrator || role == "user" && isUser && !isAdministrator {
			filtered = append(filtered, user)
		}
	}
	return filtered, nil
}
