package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIssuerURL(t *testing.T) {
	issuer, err := IssuerURL(" dev-example.us.auth0.com ")
	if err != nil || issuer.String() != "https://dev-example.us.auth0.com/" {
		t.Fatalf("issuer=%v err=%v", issuer, err)
	}
	for _, domain := range []string{"", "https://example.auth0.com", "example.auth0.com/path", "example.auth0.com:443"} {
		if _, err := IssuerURL(domain); err == nil {
			t.Fatalf("invalid domain %q accepted", domain)
		}
	}
}

func TestUserInfoClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-token" {
			t.Error("bearer token not forwarded")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sub":"auth0|student","email":"student@nus.edu.sg","email_verified":true,"nickname":"student"}`))
	}))
	defer server.Close()
	client := &UserInfoClient{endpoint: server.URL, httpClient: server.Client()}
	profile, err := client.Get(context.Background(), "secret-token")
	if err != nil || profile.Subject != "auth0|student" || !profile.EmailVerified {
		t.Fatalf("profile=%+v err=%v", profile, err)
	}
}

func TestUserInfoClientHidesResponseBodyOnFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "sensitive-profile-data", http.StatusUnauthorized)
	}))
	defer server.Close()
	client := &UserInfoClient{endpoint: server.URL, httpClient: server.Client()}
	_, err := client.Get(context.Background(), "secret-token")
	if err == nil || strings.Contains(err.Error(), "sensitive-profile-data") || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("unsafe error: %v", err)
	}
}
