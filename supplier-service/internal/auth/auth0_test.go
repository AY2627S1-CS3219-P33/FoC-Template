package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/testauth"
	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jws"
	"github.com/stretchr/testify/require"
)

type testClock struct{ nanos atomic.Int64 }

func (c *testClock) Now() time.Time      { return time.Unix(0, c.nanos.Load()) }
func (c *testClock) Add(d time.Duration) { c.nanos.Add(int64(d)) }

func verifierFixture(t *testing.T) (*Auth0, *testauth.Fixture, *testClock) {
	t.Helper()
	f := testauth.New(t)
	clock := &testClock{}
	clock.nanos.Store(time.Now().UnixNano())
	cfg := DefaultAuth0Config()
	cfg.Issuer = f.Server.URL + "/"
	cfg.ClockSkew = 0
	verifier, err := newAuth0(cfg, f.Server.Client(), clock.Now, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	return verifier, f, clock
}
func requireKind(t *testing.T, err error, kind FailureKind) {
	t.Helper()
	var failure *AuthenticationError
	require.ErrorAs(t, err, &failure)
	require.Equal(t, kind, failure.Kind)
	require.Equal(t, "authentication failed", err.Error())
}

func TestAuth0ClaimsAndPermissions_NFR3_3(t *testing.T) {
	v, f, _ := verifierFixture(t)
	tests := []struct {
		name         string
		change       func(map[string]any)
		invalid      bool
		read, manage bool
	}{
		{"read", func(c map[string]any) {}, false, true, false},
		{"admin", func(c map[string]any) { c["permissions"] = []string{"suppliers:read", "suppliers:manage"} }, false, true, true},
		{"manage only", func(c map[string]any) { c["permissions"] = []string{"suppliers:manage"} }, false, false, true},
		{"unknown permission", func(c map[string]any) { c["permissions"] = []string{"suppliers:everything"} }, false, false, false},
		{"empty permissions", func(c map[string]any) { c["permissions"] = []string{} }, false, false, false},
		{"missing permissions and spoofed role scope", func(c map[string]any) {
			delete(c, "permissions")
			c["roles"] = []string{"administrator"}
			c["scope"] = "suppliers:read suppliers:manage"
		}, false, false, false},
		{"null permissions", func(c map[string]any) { c["permissions"] = nil }, false, false, false},
		{"wrong permission type", func(c map[string]any) { c["permissions"] = "suppliers:manage" }, true, false, false},
		{"mixed permission type", func(c map[string]any) { c["permissions"] = []any{"suppliers:read", 7} }, true, false, false},
		{"null permission entry", func(c map[string]any) { c["permissions"] = []any{nil} }, true, false, false},
		{"user service audience", func(c map[string]any) { c["aud"] = "https://api.foc.local/user-service" }, true, false, false},
		{"ID token audience", func(c map[string]any) { c["aud"] = "browser-client-id" }, true, false, false},
		{"array audience", func(c map[string]any) { c["aud"] = []string{SupplierAudience, f.Server.URL + "/userinfo"} }, false, true, false},
		{"wrong array audience", func(c map[string]any) { c["aud"] = []string{"other"} }, true, false, false},
		{"wrong issuer", func(c map[string]any) { c["iss"] = "https://attacker.invalid/" }, true, false, false},
		{"issuer missing slash", func(c map[string]any) { c["iss"] = f.Server.URL }, true, false, false},
		{"expired", func(c map[string]any) { c["exp"] = time.Now().Add(-time.Minute).Unix() }, true, false, false},
		{"no expiry", func(c map[string]any) { delete(c, "exp") }, true, false, false},
		{"not yet valid", func(c map[string]any) { c["nbf"] = time.Now().Add(time.Hour).Unix() }, true, false, false},
		{"no subject", func(c map[string]any) { delete(c, "sub") }, true, false, false},
		{"empty subject", func(c map[string]any) { c["sub"] = " " }, true, false, false},
		{"bound token cannot be bearer", func(c map[string]any) { c["cnf"] = map[string]string{"jkt": "proof-required"} }, true, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claims := f.Claims(SupplierAudience, []string{"suppliers:read"})
			tt.change(claims)
			p, err := v.Authenticate(context.Background(), f.Token(t, claims, "one"))
			if tt.invalid {
				requireKind(t, err, InvalidCredential)
				require.Empty(t, p.Subject)
				return
			}
			require.NoError(t, err)
			require.Equal(t, "auth0|fixture-user", p.Subject)
			require.Equal(t, tt.read, p.Has(ReadSuppliers))
			require.Equal(t, tt.manage, p.Has(ManageSuppliers))
		})
	}
	require.EqualValues(t, 1, f.Hits.Load())
}

func TestAuth0RejectsInvalidCredentialsBeforeFetching_NFR3_3(t *testing.T) {
	v, f, _ := verifierFixture(t)
	claims := f.Claims(SupplierAudience, []string{"suppliers:read"})
	tokens := []string{"", "not-a-jwt", strings.Repeat("x", 17*1024),
		f.Token(t, claims, ""),
		testauth.Sign(t, claims, "one", []byte("not-an-rsa-key"), jwa.HS256()),
	}
	wrongIssuer := f.Claims(SupplierAudience, []string{"suppliers:read"})
	wrongIssuer["iss"] = "https://attacker.invalid/"
	tokens = append(tokens, f.Token(t, wrongIssuer, "one"))
	for _, token := range tokens {
		_, err := v.Authenticate(context.Background(), token)
		requireKind(t, err, InvalidCredential)
	}
	require.Zero(t, f.Hits.Load())
	badKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	token := testauth.Sign(t, claims, "one", badKey, jwa.RS256())
	_, err = v.Authenticate(context.Background(), token)
	requireKind(t, err, InvalidCredential)
	require.EqualValues(t, 1, f.Hits.Load())
}

func TestAuth0CacheRotationExpiryAndOutage_NFR7_1(t *testing.T) {
	v, f, clock := verifierFixture(t)
	token := f.Token(t, f.Claims(SupplierAudience, []string{"suppliers:read"}), "one")
	_, err := v.Authenticate(context.Background(), token)
	require.NoError(t, err)
	f.Response(503, []byte("sensitive-provider-response"), 0, "")
	_, err = v.Authenticate(context.Background(), token)
	require.NoError(t, err)
	require.EqualValues(t, 1, f.Hits.Load())
	clock.Add(5*time.Minute + time.Second)
	_, err = v.Authenticate(context.Background(), token)
	requireKind(t, err, VerifierUnavailable)
	require.EqualValues(t, 2, f.Hits.Load()) // Provider max-age cannot extend hard TTL.
	_, err = v.Authenticate(context.Background(), token)
	requireKind(t, err, VerifierUnavailable)
	require.EqualValues(t, 2, f.Hits.Load())
	f.Response(200, nil, 0, "")
	rotatedKey, keyErr := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, keyErr)
	f.Keys(t, map[string]*rsa.PrivateKey{"two": rotatedKey})
	clock.Add(11 * time.Second)
	rotated := testauth.Sign(t, f.Claims(SupplierAudience, []string{"suppliers:read"}), "two", rotatedKey, jwa.RS256())
	_, err = v.Authenticate(context.Background(), rotated)
	require.NoError(t, err)
	_, err = v.Authenticate(context.Background(), token)
	requireKind(t, err, InvalidCredential)
	require.EqualValues(t, 3, f.Hits.Load())
}

func TestAuth0UnknownKeyRefreshIsCoalescedAndThrottled_NFR7_1(t *testing.T) {
	v, f, clock := verifierFixture(t)
	first := f.Token(t, f.Claims(SupplierAudience, []string{"suppliers:read"}), "one")
	_, err := v.Authenticate(context.Background(), first)
	require.NoError(t, err)
	f.Keys(t, map[string]*rsa.PrivateKey{"one": f.Key, "two": f.Key})
	clock.Add(11 * time.Second)
	rotated := f.Token(t, f.Claims(SupplierAudience, []string{"suppliers:read"}), "two")
	var wg sync.WaitGroup
	results := make(chan error, 32)
	for range 32 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := v.Authenticate(context.Background(), rotated); results <- err }()
	}
	wg.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	require.EqualValues(t, 2, f.Hits.Load())
	for _, id := range []string{"unknown1", "unknown2", "unknown3"} {
		_, err := v.Authenticate(context.Background(), f.Token(t, f.Claims(SupplierAudience, []string{"suppliers:read"}), id))
		requireKind(t, err, InvalidCredential)
	}
	require.EqualValues(t, 2, f.Hits.Load())
	clock.Add(11 * time.Second)
	_, err = v.Authenticate(context.Background(), f.Token(t, f.Claims(SupplierAudience, nil), "unknown4"))
	requireKind(t, err, InvalidCredential)
	require.EqualValues(t, 3, f.Hits.Load())
	require.GreaterOrEqual(t, v.Stats().Throttled, uint64(3))
}

func TestAuth0BreakerRecoversAndDoesNotBlockKnownKeys_NFR7_1(t *testing.T) {
	v, f, clock := verifierFixture(t)
	token := f.Token(t, f.Claims(SupplierAudience, []string{"suppliers:read"}), "one")
	_, err := v.Authenticate(context.Background(), token)
	require.NoError(t, err)
	f.Response(503, []byte("secret"), 0, "")
	unknown := f.Token(t, f.Claims(SupplierAudience, nil), "rotating")
	for range 3 {
		clock.Add(11 * time.Second)
		_, err = v.Authenticate(context.Background(), unknown)
		requireKind(t, err, VerifierUnavailable)
	}
	require.True(t, v.Stats().CircuitOpen)
	_, err = v.Authenticate(context.Background(), token)
	require.NoError(t, err)
	hits := f.Hits.Load()
	clock.Add(11 * time.Second)
	_, err = v.Authenticate(context.Background(), unknown)
	requireKind(t, err, VerifierUnavailable)
	require.Equal(t, hits, f.Hits.Load())
	clock.Add(20 * time.Second)
	f.Response(200, nil, 0, "")
	f.Keys(t, map[string]*rsa.PrivateKey{"rotating": f.Key})
	_, err = v.Authenticate(context.Background(), unknown)
	require.NoError(t, err)
	require.False(t, v.Stats().CircuitOpen)
}

func TestAuth0FetchFailuresAndRedaction_NFR3_5_NFR6_3(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   []byte
		delay  time.Duration
	}{
		{"bad JSON", 200, []byte("secret-provider-body"), 0},
		{"empty keys", 200, []byte("{\"keys\":[]}"), 0},
		{"oversized", 200, []byte(strings.Repeat("x", 1024*1024+1)), 0},
		{"denied", 403, []byte("secret-provider-body"), 0},
		{"timeout", 200, nil, time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := testauth.New(t)
			f.Response(test.status, test.body, test.delay, "")
			cfg := DefaultAuth0Config()
			cfg.Issuer = f.Server.URL + "/"
			cfg.FetchTimeout = 50 * time.Millisecond
			var logs bytes.Buffer
			v, err := newAuth0(cfg, f.Server.Client(), time.Now, slog.New(slog.NewJSONHandler(&logs, nil)))
			require.NoError(t, err)
			token := f.Token(t, f.Claims(SupplierAudience, nil), "one")
			_, err = v.Authenticate(context.Background(), token)
			requireKind(t, err, VerifierUnavailable)
			require.NotContains(t, logs.String(), token)
			require.NotContains(t, logs.String(), "secret-provider-body")
			require.NotContains(t, logs.String(), "auth0|fixture-user")
		})
	}
	t.Run("TLS trust", func(t *testing.T) {
		f := testauth.New(t)
		cfg := DefaultAuth0Config()
		cfg.Issuer = f.Server.URL + "/"
		v, err := NewAuth0(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
		require.NoError(t, err)
		_, err = v.Authenticate(context.Background(), f.Token(t, f.Claims(SupplierAudience, nil), "one"))
		requireKind(t, err, VerifierUnavailable)
	})
	t.Run("redirect", func(t *testing.T) {
		v, f, _ := verifierFixture(t)
		var destinationHits atomic.Int64
		destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { destinationHits.Add(1) }))
		defer destination.Close()
		f.Response(302, nil, 0, destination.URL)
		_, err := v.Authenticate(context.Background(), f.Token(t, f.Claims(SupplierAudience, nil), "one"))
		requireKind(t, err, VerifierUnavailable)
		require.Zero(t, destinationHits.Load())
	})
}

func TestAuth0MiddlewareStatusAndPermissions_NFR3_3(t *testing.T) {
	v, f, _ := verifierFixture(t)
	reached := 0
	handler := NewMiddleware(v).RequireManage(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached++
		p, ok := PrincipalFromContext(r.Context())
		require.True(t, ok)
		require.Equal(t, "auth0|fixture-user", p.Subject)
		w.WriteHeader(204)
	}))
	for _, tt := range []struct {
		permissions []string
		status      int
	}{
		{nil, 403}, {[]string{"suppliers:read"}, 403}, {[]string{"suppliers:manage"}, 204},
	} {
		request := httptest.NewRequest("POST", "/suppliers?role=administrator", nil)
		request.Header.Set("Authorization", "Bearer "+f.Token(t, f.Claims(SupplierAudience, tt.permissions), "one"))
		request.Header.Set("X-Role", "administrator")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		require.Equal(t, tt.status, response.Code)
	}
	require.Equal(t, 1, reached)
}

func TestAuth0FetchRetriesAreBounded_NFR7_1(t *testing.T) {
	for _, tt := range []struct{ status, attempts int }{{503, 3}, {429, 3}, {403, 1}, {302, 1}} {
		t.Run(fmt.Sprint(tt.status), func(t *testing.T) {
			f := testauth.New(t)
			f.Response(tt.status, []byte("provider-body"), 0, "")
			cfg := DefaultAuth0Config()
			cfg.Issuer = f.Server.URL + "/"
			cfg.FetchAttempts = 3
			v, err := newAuth0(cfg, f.Server.Client(), time.Now, slog.New(slog.NewTextHandler(io.Discard, nil)))
			require.NoError(t, err)
			_, err = v.Authenticate(context.Background(), f.Token(t, f.Claims(SupplierAudience, nil), "one"))
			requireKind(t, err, VerifierUnavailable)
			require.EqualValues(t, tt.attempts, f.Hits.Load())
		})
	}
}

func TestAuth0CanceledWaiterDoesNotWaitForSharedFetch_NFR7_1(t *testing.T) {
	v, f, _ := verifierFixture(t)
	f.Response(200, nil, 200*time.Millisecond, "")
	result := make(chan error, 1)
	go func() { result <- v.Check(context.Background()) }()
	require.Eventually(t, func() bool { return f.Hits.Load() == 1 }, time.Second, time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := v.Check(ctx)
	require.ErrorIs(t, err, errKeysUnavailable)
	require.Less(t, time.Since(start), 150*time.Millisecond)
	require.NoError(t, <-result)
	require.EqualValues(t, 1, f.Hits.Load())
}

func TestAuth0TokenKeyURLsNeverSelectDestination_NFR3_5(t *testing.T) {
	v, f, _ := verifierFixture(t)
	var hits atomic.Int64
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer other.Close()
	headers := jws.NewHeaders()
	require.NoError(t, headers.Set(jws.KeyIDKey, "one"))
	require.NoError(t, headers.Set("jku", other.URL))
	require.NoError(t, headers.Set("x5u", other.URL))
	payload, err := json.Marshal(f.Claims(SupplierAudience, []string{"suppliers:read"}))
	require.NoError(t, err)
	token, err := jws.Sign(payload, jws.WithKey(jwa.RS256(), f.Key, jws.WithProtectedHeaders(headers)))
	require.NoError(t, err)
	_, err = v.Authenticate(context.Background(), string(token))
	require.NoError(t, err)
	require.Zero(t, hits.Load())
	require.EqualValues(t, 1, f.Hits.Load())
}
