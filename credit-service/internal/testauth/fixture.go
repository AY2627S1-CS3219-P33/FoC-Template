// Package testauth provides local signed-token fixtures; it never uses real credentials.
package testauth

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jws"
	"github.com/stretchr/testify/require"
)

type Fixture struct {
	Server   *httptest.Server
	Key      *rsa.PrivateKey
	Hits     atomic.Int64
	mu       sync.Mutex
	body     []byte
	status   int
	delay    time.Duration
	redirect string
}

func New(t *testing.T) *Fixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	f := &Fixture{Key: key, status: 200}
	f.Keys(t, map[string]*rsa.PrivateKey{"one": key})
	f.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.Hits.Add(1)
		if r.URL.Path != "/.well-known/jwks.json" || r.Header.Get("Authorization") != "" {
			http.Error(w, "unexpected request", 400)
			return
		}
		f.mu.Lock()
		body, status, delay, redirect := append([]byte(nil), f.body...), f.status, f.delay, f.redirect
		f.mu.Unlock()
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
		if redirect != "" {
			w.Header().Set("Location", redirect)
		}
		w.Header().Set("Cache-Control", "public, max-age=604800")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(f.Server.Close)
	return f
}
func (f *Fixture) Keys(t *testing.T, keys map[string]*rsa.PrivateKey) {
	t.Helper()
	set := jwk.NewSet()
	for id, key := range keys {
		public, err := jwk.Import(&key.PublicKey)
		require.NoError(t, err)
		require.NoError(t, public.Set(jwk.KeyIDKey, id))
		require.NoError(t, public.Set(jwk.AlgorithmKey, jwa.RS256()))
		require.NoError(t, public.Set(jwk.KeyUsageKey, "sig"))
		require.NoError(t, set.AddKey(public))
	}
	body, err := json.Marshal(set)
	require.NoError(t, err)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.body = body
}
func (f *Fixture) Response(status int, body []byte, delay time.Duration, redirect string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = status
	if body != nil {
		f.body = body
	}
	f.delay = delay
	f.redirect = redirect
}
func (f *Fixture) Claims(audience string, permissions any) map[string]any {
	return map[string]any{"iss": f.Server.URL + "/", "aud": audience, "sub": "auth0|fixture-user", "exp": time.Now().Add(time.Hour).Unix(), "permissions": permissions}
}
func (f *Fixture) Token(t *testing.T, claims map[string]any, kid string) string {
	return Sign(t, claims, kid, f.Key, jwa.RS256())
}
func Sign(t *testing.T, claims map[string]any, kid string, key any, algorithm jwa.SignatureAlgorithm) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	require.NoError(t, err)
	header := jws.NewHeaders()
	if kid != "" {
		require.NoError(t, header.Set(jws.KeyIDKey, kid))
	}
	signed, err := jws.Sign(payload, jws.WithKey(algorithm, key, jws.WithProtectedHeaders(header)))
	require.NoError(t, err)
	return string(signed)
}
