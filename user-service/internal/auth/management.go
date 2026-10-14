package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxManagementResponseBytes = 1 << 20

var ErrManagementUserNotFound = errors.New("Auth0 user not found")

// ManagementClient performs the limited Auth0 Management API operations needed
// to assign and remove application roles.
type ManagementClient struct {
	domain     string
	clientID   string
	secret     string
	httpClient *http.Client

	mu          sync.Mutex
	token       string
	tokenEnds   time.Time
	roleCacheMu sync.Mutex
	adminIDs    map[string]struct{}
	adminRoleID string
	adminIDsEnd time.Time
}

// ManagedUser is the small Auth0 profile subset exposed to administrators.
type ManagedUser struct {
	ID            string `json:"user_id"`
	Email         string `json:"email"`
	Name          string `json:"name,omitempty"`
	EmailVerified *bool  `json:"email_verified,omitempty"`
	CreatedAt     string `json:"created_at,omitempty"`
	LastLogin     string `json:"last_login,omitempty"`
}

type ManagedUsersPage struct {
	Users   []ManagedUser `json:"users"`
	Total   *int          `json:"total,omitempty"`
	Page    int           `json:"page"`
	PerPage int           `json:"per_page"`
	Role    string        `json:"role,omitempty"`
	Next    string        `json:"next_cursor,omitempty"`
}

type ManagedRole struct {
	ID string `json:"id"`
}

// NewManagementClient validates Auth0 configuration and creates the client.
func NewManagementClient(domain, clientID, secret string, httpClient *http.Client) (*ManagementClient, error) {
	issuer, err := IssuerURL(domain)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(clientID) == "" || strings.TrimSpace(secret) == "" || httpClient == nil {
		return nil, errors.New("Auth0 Management API credentials and HTTP client are required")
	}
	return &ManagementClient{
		domain: issuer.Hostname(), clientID: clientID, secret: secret, httpClient: httpClient,
	}, nil
}

// AssignRole assigns an existing Auth0 role to a user.
func (c *ManagementClient) AssignRole(ctx context.Context, userID, roleID string) error {
	return c.changeRole(ctx, http.MethodPost, userID, roleID)
}

// RemoveRole removes an Auth0 role from a user.
func (c *ManagementClient) RemoveRole(ctx context.Context, userID, roleID string) error {
	return c.changeRole(ctx, http.MethodDelete, userID, roleID)
}

// ListRoleUsers returns one checkpoint-paginated page of users assigned to a role.
func (c *ManagementClient) ListRoleUsers(ctx context.Context, roleID, cursor string, take int) ([]ManagedUser, string, error) {
	query := url.Values{"take": {strconv.Itoa(take)}}
	if cursor != "" {
		query.Set("from", cursor)
	}
	var result struct {
		Users []ManagedUser `json:"users"`
		Next  string        `json:"next"`
	}
	if err := c.getManagementJSON(ctx, "/api/v2/roles/"+url.PathEscape(roleID)+"/users?"+query.Encode(), &result); err != nil {
		return nil, "", err
	}
	if result.Users == nil {
		result.Users = []ManagedUser{}
	}
	return result.Users, result.Next, nil
}

// UserRoles returns the IDs of roles directly assigned to one Auth0 user.
func (c *ManagementClient) UserRoles(ctx context.Context, userID string) ([]ManagedRole, error) {
	if strings.TrimSpace(userID) == "" {
		return nil, errors.New("Auth0 user ID is required")
	}
	var roles []ManagedRole
	if err := c.getManagementJSON(ctx, "/api/v2/users/"+url.PathEscape(userID)+"/roles", &roles); err != nil {
		return nil, err
	}
	return roles, nil
}

// AdministratorIDs returns a short-lived snapshot used to exclude administrators
// from the regular-user directory when users have both roles assigned.
func (c *ManagementClient) AdministratorIDs(ctx context.Context, roleID string) (map[string]struct{}, error) {
	c.roleCacheMu.Lock()
	defer c.roleCacheMu.Unlock()
	if c.adminRoleID == roleID && time.Now().Before(c.adminIDsEnd) {
		return c.adminIDs, nil
	}
	ids := make(map[string]struct{})
	cursor := ""
	for {
		users, next, err := c.ListRoleUsers(ctx, roleID, cursor, 100)
		if err != nil {
			return nil, err
		}
		for _, user := range users {
			ids[user.ID] = struct{}{}
		}
		if next == "" || next == cursor {
			break
		}
		cursor = next
	}
	// shortcut: Dashboard role edits may stay cached for one minute; lower the TTL if those changes need to appear immediately.
	c.adminIDs, c.adminRoleID, c.adminIDsEnd = ids, roleID, time.Now().Add(time.Minute)
	return ids, nil
}

// ListUsers searches Auth0 users by an optional exact email and returns one page.
func (c *ManagementClient) ListUsers(ctx context.Context, page, perPage int, email string) (ManagedUsersPage, error) {
	query := url.Values{}
	query.Set("page", strconv.Itoa(page))
	query.Set("per_page", strconv.Itoa(perPage))
	query.Set("include_totals", "true")
	query.Set("fields", "user_id,email,name,email_verified,created_at,last_login")
	query.Set("include_fields", "true")
	if email != "" {
		escaped := strings.NewReplacer("\\", "\\\\", `"`, `\"`).Replace(email)
		query.Set("q", `email:"`+escaped+`"`)
		query.Set("search_engine", "v3")
	}
	token, err := c.managementToken(ctx)
	if err != nil {
		return ManagedUsersPage{}, err
	}
	endpoint := "https://" + c.domain + "/api/v2/users?" + query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ManagedUsersPage{}, errors.New("create Auth0 user-list request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := c.httpClient.Do(req)
	if err != nil {
		return ManagedUsersPage{}, errors.New("call Auth0 Management API")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxManagementResponseBytes))
		return ManagedUsersPage{}, fmt.Errorf("Auth0 Management API returned status %d", response.StatusCode)
	}
	var result struct {
		Users []ManagedUser `json:"users"`
		Total int           `json:"total"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxManagementResponseBytes)).Decode(&result); err != nil {
		return ManagedUsersPage{}, errors.New("decode Auth0 user-list response")
	}
	if result.Users == nil {
		result.Users = []ManagedUser{}
	}
	return ManagedUsersPage{Users: result.Users, Total: &result.Total, Page: page, PerPage: perPage}, nil
}

func (c *ManagementClient) getManagementJSON(ctx context.Context, path string, destination any) error {
	token, err := c.managementToken(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+c.domain+path, nil)
	if err != nil {
		return errors.New("create Auth0 Management API request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := c.httpClient.Do(req)
	if err != nil {
		return errors.New("call Auth0 Management API")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxManagementResponseBytes))
		return fmt.Errorf("Auth0 Management API returned status %d", response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxManagementResponseBytes)).Decode(destination); err != nil {
		return errors.New("decode Auth0 Management API response")
	}
	return nil
}

func (c *ManagementClient) changeRole(ctx context.Context, method, userID, roleID string) error {
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(roleID) == "" {
		return errors.New("Auth0 user and role IDs are required")
	}
	token, err := c.managementToken(ctx)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string][]string{"roles": {roleID}})
	if err != nil {
		return errors.New("encode Auth0 role request")
	}
	endpoint := "https://" + c.domain + "/api/v2/users/" + url.PathEscape(userID) + "/roles"
	req, err := http.NewRequestWithContext(ctx, method, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return errors.New("create Auth0 role request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient.Do(req)
	if err != nil {
		return errors.New("call Auth0 Management API")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxManagementResponseBytes))
	if response.StatusCode == http.StatusNotFound {
		return ErrManagementUserNotFound
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Auth0 Management API returned status %d", response.StatusCode)
	}
	c.roleCacheMu.Lock()
	c.adminIDsEnd = time.Time{}
	c.roleCacheMu.Unlock()
	return nil
}

func (c *ManagementClient) managementToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Add(time.Minute).Before(c.tokenEnds) {
		return c.token, nil
	}
	payload, err := json.Marshal(map[string]string{
		"grant_type":    "client_credentials",
		"client_id":     c.clientID,
		"client_secret": c.secret,
		"audience":      "https://" + c.domain + "/api/v2/",
	})
	if err != nil {
		return "", errors.New("encode Auth0 token request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+c.domain+"/oauth/token", strings.NewReader(string(payload)))
	if err != nil {
		return "", errors.New("create Auth0 token request")
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient.Do(req)
	if err != nil {
		return "", errors.New("request Auth0 Management API token")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxManagementResponseBytes))
		return "", errors.New("Auth0 rejected Management API token request")
	}
	var result struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxManagementResponseBytes)).Decode(&result); err != nil || result.AccessToken == "" || result.ExpiresIn <= 0 {
		return "", errors.New("decode Auth0 Management API token")
	}
	c.token = result.AccessToken
	c.tokenEnds = time.Now().Add(time.Duration(result.ExpiresIn) * time.Second)
	return c.token, nil
}
