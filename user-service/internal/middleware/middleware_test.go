package middleware

import (
	"bytes"
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/auth0/go-jwt-middleware/v3/core"
	"github.com/auth0/go-jwt-middleware/v3/validator"
)

func TestNewAuth0RejectsInvalidConfiguration(t *testing.T) {
	for _, test := range []struct{ domain, audience string }{
		{"", "audience"}, {"https://example.auth0.com", "audience"}, {"example.auth0.com", ""},
	} {
		if _, err := NewAuth0(test.domain, test.audience); err == nil {
			t.Fatalf("accepted domain=%q audience=%q", test.domain, test.audience)
		}
	}
}

func TestSubjectAndBearerToken(t *testing.T) {
	claims := &validator.ValidatedClaims{RegisteredClaims: validator.RegisteredClaims{Subject: "auth0|student"}}
	ctx := core.SetClaims(context.Background(), claims)
	if subject, ok := Subject(ctx); !ok || subject != "auth0|student" {
		t.Fatalf("subject=%q ok=%v", subject, ok)
	}
	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Set("Authorization", "Bearer access-token")
	if token, ok := BearerToken(request); !ok || token != "access-token" {
		t.Fatalf("token=%q ok=%v", token, ok)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestAuthenticationValidatesJWT(t *testing.T) {
	const (
		domain   = "example.auth0.com"
		issuer   = "https://example.auth0.com/"
		audience = "https://api.example.com"
		keyID    = "test-key"
	)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwksJSON, err := json.Marshal(map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "kid": keyID, "use": "sig", "alg": "RS256",
		"n": base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes()),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	discoveryJSON, err := json.Marshal(map[string]string{
		"issuer": issuer, "jwks_uri": issuer + ".well-known/jwks.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body []byte
		switch request.URL.Path {
		case "/.well-known/openid-configuration":
			body = discoveryJSON
		case "/.well-known/jwks.json":
			body = jwksJSON
		default:
			return nil, fmt.Errorf("unexpected JWKS request: %s", request.URL)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader(body)),
			Request:    request,
		}, nil
	})}
	authentication, err := newAuth0(domain, audience, httpClient)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	validClaims := map[string]any{
		"sub": "auth0|student", "iss": issuer, "aud": audience,
		"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(time.Hour).Unix(),
	}
	tests := []struct {
		name       string
		algorithm  string
		change     func(map[string]any)
		wantStatus int
	}{
		{name: "valid", algorithm: "RS256", wantStatus: http.StatusNoContent},
		{name: "issuer", algorithm: "RS256", change: func(claims map[string]any) { claims["iss"] = "https://other.example.com/" }, wantStatus: http.StatusUnauthorized},
		{name: "audience", algorithm: "RS256", change: func(claims map[string]any) { claims["aud"] = "https://other-api.example.com" }, wantStatus: http.StatusUnauthorized},
		{name: "expired", algorithm: "RS256", change: func(claims map[string]any) { claims["exp"] = now.Add(-time.Hour).Unix() }, wantStatus: http.StatusUnauthorized},
		{name: "algorithm", algorithm: "HS256", wantStatus: http.StatusUnauthorized},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			claims := make(map[string]any, len(validClaims))
			for name, value := range validClaims {
				claims[name] = value
			}
			if test.change != nil {
				test.change(claims)
			}
			token := signedToken(t, key, keyID, test.algorithm, claims)
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.Header.Set("Authorization", "Bearer "+token)
			response := httptest.NewRecorder()
			authentication.Authentication(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				subject, ok := Subject(r.Context())
				if !ok || subject != "auth0|student" {
					t.Errorf("subject=%q ok=%v", subject, ok)
				}
				w.WriteHeader(http.StatusNoContent)
			})).ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status=%d body=%q want=%d", response.Code, response.Body.String(), test.wantStatus)
			}
			if test.wantStatus == http.StatusUnauthorized && !strings.Contains(response.Body.String(), "invalid_token") {
				t.Fatalf("body=%q", response.Body.String())
			}
		})
	}
}

func signedToken(t *testing.T, key *rsa.PrivateKey, keyID, algorithm string, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]string{"alg": algorithm, "kid": keyID, "typ": "JWT"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	var signature []byte
	if algorithm == "HS256" {
		mac := hmac.New(sha256.New, []byte("test-secret"))
		_, _ = mac.Write([]byte(encoded))
		signature = mac.Sum(nil)
	} else {
		digest := sha256.Sum256([]byte(encoded))
		signature, err = rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
		if err != nil {
			t.Fatal(err)
		}
	}
	return encoded + "." + base64.RawURLEncoding.EncodeToString(signature)
}
