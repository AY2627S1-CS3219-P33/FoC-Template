package serviceauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	testSecret   = "s3cr3t-client-value"
	testClientID = "supplier-service-client"
	testAudience = "https://api.foc.local/order-service"
)

type testClock struct{ nanos atomic.Int64 }

func (c *testClock) Now() time.Time      { return time.Unix(0, c.nanos.Load()) }
func (c *testClock) Add(d time.Duration) { c.nanos.Add(int64(d)) }

// tokenStub is a local Auth0 /oauth/token endpoint. It issues "token-<n>" for
// the n-th request unless a raw response is configured.
type tokenStub struct {
	server  *httptest.Server
	hits    atomic.Int64
	entered chan struct{}

	mu        sync.Mutex
	status    int
	body      string
	expiresIn int
	location  string
	release   chan struct{}
}

func newTokenStub(t *testing.T) *tokenStub {
	t.Helper()
	s := &tokenStub{status: http.StatusOK, expiresIn: 120, entered: make(chan struct{}, 1)}
	s.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := s.hits.Add(1)
		select {
		case s.entered <- struct{}{}:
		default:
		}
		if r.Method != http.MethodPost || r.URL.Path != "/oauth/token" || r.Header.Get("Authorization") != "" ||
			r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Errorf("unexpected token request %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		if err := r.ParseForm(); err != nil || r.URL.RawQuery != "" ||
			r.PostForm.Get("grant_type") != "client_credentials" ||
			r.PostForm.Get("client_id") != testClientID ||
			r.PostForm.Get("client_secret") != testSecret ||
			r.PostForm.Get("audience") != testAudience {
			t.Errorf("unexpected token request form")
			http.Error(w, "unexpected form", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		status, body, expiresIn, location, release := s.status, s.body, s.expiresIn, s.location, s.release
		s.mu.Unlock()
		if release != nil {
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		if body == "" {
			encoded, _ := json.Marshal(map[string]any{
				"access_token": "token-" + strconv.FormatInt(n, 10),
				"token_type":   "Bearer",
				"expires_in":   expiresIn,
			})
			body = string(encoded)
		}
		if location != "" {
			w.Header().Set("Location", location)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(s.server.Close)
	return s
}

func (s *tokenStub) respond(status int, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status, s.body = status, body
}

func (s *tokenStub) hold() chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.release = make(chan struct{})
	return s.release
}

func (s *tokenStub) config() Config {
	return Config{Issuer: s.server.URL + "/", ClientID: testClientID, ClientSecret: testSecret, Audience: testAudience}
}

func newSource(t *testing.T, stub *tokenStub) (*ClientCredentials, *testClock) {
	t.Helper()
	clock := &testClock{}
	clock.nanos.Store(time.Now().UnixNano())
	src, err := newClientCredentials(stub.config(), stub.server.Client(), clock.Now)
	require.NoError(t, err)
	return src, clock
}

func TestClientCredentialsCachesToken_NFR3_5_2(t *testing.T) {
	stub := newTokenStub(t)
	src, _ := newSource(t, stub)

	for range 3 {
		token, err := src.Token(context.Background())
		require.NoError(t, err)
		require.Equal(t, "token-1", token)
	}
	require.EqualValues(t, 1, stub.hits.Load())
}

func TestClientCredentialsRefreshesSixtySecondsBeforeExpiry(t *testing.T) {
	stub := newTokenStub(t)
	src, clock := newSource(t, stub)

	token, err := src.Token(context.Background())
	require.NoError(t, err)
	require.Equal(t, "token-1", token)

	// expires_in is 120s, so the token is reused until 60s remain.
	clock.Add(59 * time.Second)
	token, err = src.Token(context.Background())
	require.NoError(t, err)
	require.Equal(t, "token-1", token)

	clock.Add(time.Second)
	token, err = src.Token(context.Background())
	require.NoError(t, err)
	require.Equal(t, "token-2", token)

	token, err = src.Token(context.Background())
	require.NoError(t, err)
	require.Equal(t, "token-2", token)
	require.EqualValues(t, 2, stub.hits.Load())
}

func TestClientCredentialsShortLivedTokenIsNotCached(t *testing.T) {
	stub := newTokenStub(t)
	stub.mu.Lock()
	stub.expiresIn = 60
	stub.mu.Unlock()
	src, _ := newSource(t, stub)

	for i := 1; i <= 2; i++ {
		token, err := src.Token(context.Background())
		require.NoError(t, err)
		require.Equal(t, fmt.Sprintf("token-%d", i), token)
	}
}

func TestClientCredentialsConcurrentCallersShareOneFetch(t *testing.T) {
	stub := newTokenStub(t)
	release := stub.hold()
	src, _ := newSource(t, stub)

	const callers = 32
	tokens := make([]string, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tokens[i], errs[i] = src.Token(context.Background())
		}()
	}
	<-stub.entered
	// Hold the first fetch open so the other callers arrive while it is in
	// flight rather than finding the token already cached.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	for i := range callers {
		require.NoError(t, errs[i])
		require.Equal(t, "token-1", tokens[i])
	}
	require.EqualValues(t, 1, stub.hits.Load())
}

func TestClientCredentialsCallerCancelDoesNotAbortSharedFetch(t *testing.T) {
	stub := newTokenStub(t)
	release := stub.hold()
	src, _ := newSource(t, stub)

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderErr := make(chan error, 1)
	go func() {
		_, err := src.Token(leaderCtx)
		leaderErr <- err
	}()
	<-stub.entered

	type result struct {
		token string
		err   error
	}
	follower := make(chan result, 1)
	go func() {
		token, err := src.Token(context.Background())
		follower <- result{token, err}
	}()

	cancelLeader()
	require.ErrorIs(t, <-leaderErr, context.Canceled)
	close(release)

	got := <-follower
	require.NoError(t, got.err)
	require.Equal(t, "token-1", got.token)
	require.EqualValues(t, 1, stub.hits.Load())
}

func TestClientCredentialsFailuresAreNotCachedAndDoNotLeak_NFR3_4_3(t *testing.T) {
	leaky := `{"error":"access_denied","client_secret":"` + testSecret + `","access_token":"leaked-token"}`
	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{name: "rejected", status: http.StatusUnauthorized, body: leaky, want: "status 401"},
		{name: "server error", status: http.StatusInternalServerError, body: leaky, want: "status 500"},
		{name: "malformed json", status: http.StatusOK, body: "not json " + testSecret, want: "malformed response"},
		{name: "missing token", status: http.StatusOK, body: `{"token_type":"Bearer","expires_in":120}`, want: "lacks a bearer token"},
		{name: "missing expiry", status: http.StatusOK, body: `{"access_token":"leaked-token","token_type":"Bearer"}`, want: "lacks a bearer token"},
		{name: "wrong token type", status: http.StatusOK, body: `{"access_token":"leaked-token","token_type":"mac","expires_in":120}`, want: "lacks a bearer token"},
		{name: "header injection", status: http.StatusOK, body: `{"access_token":"leaked-token\r\nX-Evil: 1","expires_in":120}`, want: "lacks a bearer token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := newTokenStub(t)
			stub.respond(tt.status, tt.body)
			src, _ := newSource(t, stub)

			for range 2 {
				_, err := src.Token(context.Background())
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.want)
				require.NotContains(t, err.Error(), testSecret)
				require.NotContains(t, err.Error(), "leaked-token")
			}
			require.EqualValues(t, 2, stub.hits.Load())
		})
	}

	t.Run("redirect is not followed", func(t *testing.T) {
		var destinationHits atomic.Int64
		destination := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { destinationHits.Add(1) }))
		defer destination.Close()
		stub := newTokenStub(t)
		stub.respond(http.StatusTemporaryRedirect, "{}")
		stub.mu.Lock()
		stub.location = destination.URL + "/oauth/token"
		stub.mu.Unlock()
		src, _ := newSource(t, stub)

		_, err := src.Token(context.Background())
		require.ErrorContains(t, err, "status 307")
		require.Zero(t, destinationHits.Load())
	})

	t.Run("unreachable", func(t *testing.T) {
		stub := newTokenStub(t)
		src, _ := newSource(t, stub)
		stub.server.Close()

		_, err := src.Token(context.Background())
		require.ErrorContains(t, err, "token request failed")
		require.NotContains(t, err.Error(), testSecret)
	})
}

func TestConfigValidate(t *testing.T) {
	valid := Config{Issuer: "https://tenant.auth0.com/", ClientID: testClientID, ClientSecret: testSecret, Audience: testAudience}
	require.NoError(t, valid.Validate())

	tests := map[string]func(*Config){
		"http issuer":          func(c *Config) { c.Issuer = "http://tenant.auth0.com/" },
		"issuer without slash": func(c *Config) { c.Issuer = "https://tenant.auth0.com" },
		"issuer with path":     func(c *Config) { c.Issuer = "https://tenant.auth0.com/oauth/" },
		"issuer with userinfo": func(c *Config) { c.Issuer = "https://" + testSecret + "@tenant.auth0.com/" },
		"missing client ID":    func(c *Config) { c.ClientID = " " },
		"missing secret":       func(c *Config) { c.ClientSecret = "" },
		"missing audience":     func(c *Config) { c.Audience = "" },
		"audience list":        func(c *Config) { c.Audience = "a,b" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := valid
			mutate(&cfg)
			err := cfg.Validate()
			require.Error(t, err)
			require.NotContains(t, err.Error(), testSecret)
			_, err = NewClientCredentials(cfg, nil)
			require.Error(t, err)
		})
	}
}

func TestSecretAndTokenAreRedactedWhenFormatted(t *testing.T) {
	stub := newTokenStub(t)
	stub.respond(http.StatusOK, `{"access_token":"cached-token-value","token_type":"Bearer","expires_in":3600}`)
	src, _ := newSource(t, stub)
	_, err := src.Token(context.Background())
	require.NoError(t, err)
	cfg := stub.config()

	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	logger.Info("config", "cfg", cfg)
	logger.Info("source", "src", src)
	formatted := fmt.Sprintf("%v %+v %#v %s %v %+v %#v", cfg, cfg, cfg, cfg, src, src, src) + logs.String()

	require.Contains(t, formatted, testClientID)
	require.Contains(t, formatted, "[REDACTED]")
	require.NotContains(t, formatted, testSecret)
	require.NotContains(t, formatted, "cached-token-value")
}
