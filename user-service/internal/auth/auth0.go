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
)

const maxUserInfoResponseBytes = 1 << 20

// UserInfo contains trusted identity attributes returned by Auth0 /userinfo.
type UserInfo struct {
	Subject       string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Nickname      string `json:"nickname"`
	Name          string `json:"name"`
}

// UserInfoReader resolves the profile associated with a user access token.
type UserInfoReader interface {
	Get(ctx context.Context, accessToken string) (UserInfo, error)
}

type UserInfoClient struct {
	endpoint   string
	httpClient *http.Client
}

func NewUserInfoClient(domain string, httpClient *http.Client) (*UserInfoClient, error) {
	issuer, err := IssuerURL(domain)
	if err != nil {
		return nil, err
	}
	if httpClient == nil {
		return nil, errors.New("HTTP client is required")
	}
	return &UserInfoClient{endpoint: issuer.ResolveReference(&url.URL{Path: "userinfo"}).String(), httpClient: httpClient}, nil
}

func (c *UserInfoClient) Get(ctx context.Context, accessToken string) (UserInfo, error) {
	if strings.TrimSpace(accessToken) == "" {
		return UserInfo{}, errors.New("access token is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint, nil)
	if err != nil {
		return UserInfo{}, errors.New("create Auth0 user profile request")
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	response, err := c.httpClient.Do(req)
	if err != nil {
		return UserInfo{}, errors.New("retrieve Auth0 user profile")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxUserInfoResponseBytes))
		return UserInfo{}, fmt.Errorf("retrieve Auth0 user profile: status %d", response.StatusCode)
	}
	var profile UserInfo
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxUserInfoResponseBytes))
	if err := decoder.Decode(&profile); err != nil {
		return UserInfo{}, errors.New("decode Auth0 user profile")
	}
	return profile, nil
}

// IssuerURL validates an Auth0 tenant hostname and constructs its HTTPS issuer.
func IssuerURL(domain string) (*url.URL, error) {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return nil, errors.New("Auth0 domain is required")
	}
	if strings.Contains(domain, "://") || strings.ContainsAny(domain, "/?#") {
		return nil, errors.New("Auth0 domain must be a hostname without scheme or path")
	}
	issuer, err := url.Parse("https://" + domain + "/")
	if err != nil || issuer.Hostname() == "" || issuer.User != nil || issuer.Port() != "" {
		return nil, errors.New("Auth0 domain is invalid")
	}
	return issuer, nil
}
