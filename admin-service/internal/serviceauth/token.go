package serviceauth

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

	"golang.org/x/sync/singleflight"
)

// TokenSource returns a bearer token for outgoing service calls.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

const (
	// refreshMargin is how long before expiry a cached token is replaced, so
	// a token never expires while a request carrying it is in flight.
	refreshMargin = 60 * time.Second
	// maxTokenLifetime bounds a provider-supplied expires_in.
	maxTokenLifetime = 24 * time.Hour
	defaultTimeout   = 5 * time.Second
	maxResponseBytes = 64 << 10
)

// ClientCredentials obtains Auth0 machine-to-machine tokens for one audience.
// It is safe for concurrent use.
type ClientCredentials struct {
	cfg      Config
	tokenURL string
	client   *http.Client
	now      func() time.Time
	group    singleflight.Group

	mu      sync.Mutex
	token   string
	expires time.Time
}

// NewClientCredentials builds a token source without contacting Auth0. A nil
// client uses http.DefaultTransport. The client is copied; a zero timeout is
// replaced with a default and redirects are never followed, because the
// request body carries the client secret.
func NewClientCredentials(cfg Config, client *http.Client) (*ClientCredentials, error) {
	return newClientCredentials(cfg, client, time.Now)
}

func newClientCredentials(cfg Config, client *http.Client, now func() time.Time) (*ClientCredentials, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if client == nil {
		client = &http.Client{}
	}
	httpClient := *client
	if httpClient.Timeout <= 0 {
		httpClient.Timeout = defaultTimeout
	}
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	issuer, _ := url.Parse(cfg.Issuer)
	tokenURL := issuer.ResolveReference(&url.URL{Path: "/oauth/token"})
	return &ClientCredentials{cfg: cfg, tokenURL: tokenURL.String(), client: &httpClient, now: now}, nil
}

// Token returns the cached token, or fetches a new one when none is cached or
// the cached one is within refreshMargin of expiry. Concurrent callers share
// one fetch.
func (s *ClientCredentials) Token(ctx context.Context) (string, error) {
	if token, ok := s.cached(); ok {
		return token, nil
	}
	// The fetch is shared, so whichever caller starts it must not be able to
	// cancel it for the others; the client timeout bounds it instead. Each
	// caller still stops waiting when its own context ends.
	result := s.group.DoChan("token", func() (any, error) {
		if token, ok := s.cached(); ok {
			return token, nil
		}
		return s.fetch(context.WithoutCancel(ctx))
	})
	select {
	case r := <-result:
		if r.Err != nil {
			return "", r.Err
		}
		return r.Val.(string), nil
	case <-ctx.Done():
		return "", fmt.Errorf("serviceauth: waiting for token: %w", ctx.Err())
	}
}

func (s *ClientCredentials) cached() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token == "" || !s.now().Before(s.expires.Add(-refreshMargin)) {
		return "", false
	}
	return s.token, true
}

func (s *ClientCredentials) fetch(ctx context.Context) (string, error) {
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {s.cfg.ClientID},
		"client_secret": {s.cfg.ClientSecret},
		"audience":      {s.cfg.Audience},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", errors.New("serviceauth: build token request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	// Measure lifetime from before the request so the cached expiry is never late.
	issuedAt := s.now()
	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("serviceauth: token request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// The body may echo request fields, so only the status is reported.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		return "", fmt.Errorf("serviceauth: token endpoint returned status %d", resp.StatusCode)
	}

	var body struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&body); err != nil {
		return "", errors.New("serviceauth: token endpoint returned a malformed response")
	}
	if body.AccessToken == "" || strings.ContainsAny(body.AccessToken, " \t\r\n") ||
		body.ExpiresIn <= 0 ||
		(body.TokenType != "" && !strings.EqualFold(body.TokenType, "Bearer")) {
		return "", errors.New("serviceauth: token endpoint response lacks a bearer token and expiry")
	}

	lifetime := maxTokenLifetime
	if body.ExpiresIn < int64(maxTokenLifetime/time.Second) {
		lifetime = time.Duration(body.ExpiresIn) * time.Second
	}
	s.mu.Lock()
	s.token, s.expires = body.AccessToken, issuedAt.Add(lifetime)
	s.mu.Unlock()
	return body.AccessToken, nil
}

// String omits the client secret and the cached token, which %v would
// otherwise print from the unexported fields.
func (s *ClientCredentials) String() string {
	return "serviceauth.ClientCredentials{" + s.cfg.String() + "}"
}

// GoString omits the client secret and the cached token for %#v.
func (s *ClientCredentials) GoString() string { return s.String() }
