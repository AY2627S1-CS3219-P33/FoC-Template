// Package credit calls the credit service on behalf of user-service using an
// Auth0 machine-to-machine token.
package credit

import (
	"bytes"
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

const (
	refreshMargin    = 60 * time.Second
	maxResponseBytes = 64 << 10
)

// Config identifies user-service to Auth0 and locates the credit service.
type Config struct {
	// BaseURL is the credit service origin, for example "http://credit-service:8080".
	BaseURL string
	// TokenURL is the Auth0 token endpoint, for example "https://tenant.auth0.com/oauth/token".
	TokenURL     string
	ClientID     string
	ClientSecret string
	// Audience is the credit service API identifier.
	Audience string
}

// Client creates wallets in the credit service. It is safe for concurrent use.
type Client struct {
	cfg  Config
	http *http.Client
	now  func() time.Time

	mu      sync.Mutex
	token   string
	expires time.Time
}

// NewClient validates cfg without contacting any service. Redirects are never
// followed because the token request body carries the client secret.
func NewClient(cfg Config, httpClient *http.Client) (*Client, error) {
	if httpClient == nil {
		return nil, errors.New("credit: HTTP client is required")
	}
	for name, raw := range map[string]string{"base URL": cfg.BaseURL, "token URL": cfg.TokenURL} {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
			return nil, fmt.Errorf("credit: %s must be an absolute HTTP(S) URL", name)
		}
	}
	if cfg.ClientID == "" || cfg.ClientSecret == "" || cfg.Audience == "" {
		return nil, errors.New("credit: client ID, client secret and audience are required")
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	c := *httpClient
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{cfg: cfg, http: &c, now: time.Now}, nil
}

// EnsureWallet creates the owner's wallet, or confirms it exists. The credit
// service treats the call as idempotent per owner, so repeating it is safe and
// never allocates credits twice.
func (c *Client) EnsureWallet(ctx context.Context, ownerID, auth0Subject string) error {
	token, err := c.bearer(ctx)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]string{"auth0Subject": auth0Subject})
	endpoint := c.cfg.BaseURL + "/internal/wallets/" + url.PathEscape(ownerID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(body))
	if err != nil {
		return errors.New("credit: build wallet request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("credit: create wallet: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("credit: create wallet returned status %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) bearer(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && c.now().Before(c.expires.Add(-refreshMargin)) {
		return c.token, nil
	}
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {c.cfg.ClientID},
		"client_secret": {c.cfg.ClientSecret},
		"audience":      {c.cfg.Audience},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", errors.New("credit: build token request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	issuedAt := c.now()
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("credit: token request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// The body may echo request fields, so only the status is reported.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		return "", fmt.Errorf("credit: token endpoint returned status %d", resp.StatusCode)
	}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&out); err != nil ||
		out.AccessToken == "" || out.ExpiresIn <= 0 {
		return "", errors.New("credit: token endpoint returned a malformed response")
	}
	c.token = out.AccessToken
	c.expires = issuedAt.Add(time.Duration(out.ExpiresIn) * time.Second)
	return c.token, nil
}
