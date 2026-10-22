package credit

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestClient(t *testing.T, creditStatus int) (*Client, *int, *int) {
	t.Helper()
	var tokenCalls, walletCalls int
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenCalls++
		r.ParseForm()
		if r.Form.Get("grant_type") != "client_credentials" || r.Form.Get("audience") != "aud" {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		w.Write([]byte(`{"access_token":"tok","expires_in":3600}`))
	}))
	cs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		walletCalls++
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPut || r.URL.Path != "/internal/wallets/owner-1" ||
			r.Header.Get("Authorization") != "Bearer tok" ||
			!strings.Contains(string(body), `"auth0Subject":"auth0|x"`) {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		w.WriteHeader(creditStatus)
	}))
	t.Cleanup(auth.Close)
	t.Cleanup(cs.Close)
	c, err := NewClient(Config{BaseURL: cs.URL, TokenURL: auth.URL, ClientID: "id", ClientSecret: "s", Audience: "aud"}, &http.Client{})
	if err != nil {
		t.Fatal(err)
	}
	return c, &tokenCalls, &walletCalls
}

func TestEnsureWalletAcceptsCreatedAndExisting(t *testing.T) {
	for _, status := range []int{http.StatusCreated, http.StatusOK} {
		c, _, walletCalls := newTestClient(t, status)
		if err := c.EnsureWallet(context.Background(), "owner-1", "auth0|x"); err != nil {
			t.Fatalf("status %d: %v", status, err)
		}
		if *walletCalls != 1 {
			t.Fatalf("wallet calls = %d", *walletCalls)
		}
	}
}

func TestEnsureWalletReusesCachedToken(t *testing.T) {
	c, tokenCalls, _ := newTestClient(t, http.StatusOK)
	for i := 0; i < 3; i++ {
		if err := c.EnsureWallet(context.Background(), "owner-1", "auth0|x"); err != nil {
			t.Fatal(err)
		}
	}
	if *tokenCalls != 1 {
		t.Fatalf("token calls = %d, want 1", *tokenCalls)
	}
}

func TestEnsureWalletRejectsErrorStatus(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusConflict, http.StatusServiceUnavailable} {
		c, _, _ := newTestClient(t, status)
		if err := c.EnsureWallet(context.Background(), "owner-1", "auth0|x"); err == nil {
			t.Fatalf("status %d: expected error", status)
		}
	}
}

func TestNewClientValidates(t *testing.T) {
	good := Config{BaseURL: "http://c", TokenURL: "https://t/oauth/token", ClientID: "i", ClientSecret: "s", Audience: "a"}
	if _, err := NewClient(good, &http.Client{}); err != nil {
		t.Fatal(err)
	}
	bad := good
	bad.BaseURL = "credit"
	if _, err := NewClient(bad, &http.Client{}); err == nil {
		t.Fatal("expected error for relative base URL")
	}
	bad = good
	bad.ClientSecret = ""
	if _, err := NewClient(bad, &http.Client{}); err == nil {
		t.Fatal("expected error for missing secret")
	}
}
