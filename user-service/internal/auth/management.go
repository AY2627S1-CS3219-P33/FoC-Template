package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

	mu        sync.Mutex
	token     string
	tokenEnds time.Time
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
