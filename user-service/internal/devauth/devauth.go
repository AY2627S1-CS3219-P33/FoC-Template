// Package devauth is a DEV-ONLY, in-process mock of an Auth0 OIDC issuer.
//
// It exists so the service can be exercised end to end (login -> provision ->
// authenticated APIs) without a real Auth0 tenant. It generates an RSA signing
// key, serves the OIDC discovery document, JWKS, and /userinfo through an
// in-memory HTTP client transport, and mints RS256 access tokens signed with
// that key. Never enable this outside local development: it will happily sign a
// token for any subject.
package devauth

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"

	"user-service/internal/auth"
)

const keyID = "dev-mock-key"

// Mock is a self-contained fake issuer for local development.
type Mock struct {
	issuer   string
	audience string
	key      *rsa.PrivateKey
	jwks     []byte
}

// New builds a mock issuer for the given Auth0 domain and API audience. The
// issuer URL matches production expectations: https://<domain>/.
func New(domain, audience string) (*Mock, error) {
	issuer, err := auth.IssuerURL(domain)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(audience) == "" {
		return nil, errors.New("audience is required")
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	jwks, err := json.Marshal(map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "kid": keyID, "use": "sig", "alg": "RS256",
		"n": base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes()),
	}}})
	if err != nil {
		return nil, err
	}
	return &Mock{issuer: issuer.String(), audience: audience, key: key, jwks: jwks}, nil
}

// Client returns an HTTP client whose transport answers the OIDC discovery,
// JWKS, and /userinfo endpoints from memory. Wire it into the auth middleware
// and the userinfo reader so no network call leaves the process.
func (m *Mock) Client() *http.Client {
	return &http.Client{Transport: roundTripFunc(m.serve)}
}

func (m *Mock) serve(request *http.Request) (*http.Response, error) {
	switch request.URL.Path {
	case "/.well-known/openid-configuration":
		discovery, _ := json.Marshal(map[string]string{
			"issuer": m.issuer, "jwks_uri": m.issuer + ".well-known/jwks.json",
			"userinfo_endpoint": m.issuer + "userinfo",
		})
		return jsonResponse(request, discovery), nil
	case "/.well-known/jwks.json":
		return jsonResponse(request, m.jwks), nil
	case "/userinfo":
		return m.userinfo(request)
	default:
		return nil, fmt.Errorf("devauth: unexpected request: %s", request.URL)
	}
}

// userinfo echoes the identity claims embedded in the presented access token,
// mirroring the shape of a real Auth0 /userinfo response.
func (m *Mock) userinfo(request *http.Request) (*http.Response, error) {
	scheme, token, ok := strings.Cut(request.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return &http.Response{StatusCode: http.StatusUnauthorized, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{"error":"unauthorized"}`)), Request: request}, nil
	}
	claims, err := decodeClaims(token)
	if err != nil {
		return nil, err
	}
	info, _ := json.Marshal(auth.UserInfo{
		Subject:       asString(claims["sub"]),
		Email:         asString(claims["email"]),
		EmailVerified: claims["email_verified"] == true,
		Nickname:      asString(claims["nickname"]),
		Name:          asString(claims["name"]),
	})
	return jsonResponse(request, info), nil
}

// Mint signs an RS256 access token carrying the identity claims the provisioner
// needs. The email must be an eligible domain for provisioning to succeed.
func (m *Mock) Mint(sub, email, nickname, name string, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := map[string]any{
		"sub": sub, "iss": m.issuer, "aud": m.audience,
		"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(ttl).Unix(),
		"email": email, "email_verified": true, "nickname": nickname, "name": name,
	}
	header, err := json.Marshal(map[string]string{"alg": "RS256", "kid": keyID, "typ": "JWT"})
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, m.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func decodeClaims(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("devauth: malformed token")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		return nil, err
	}
	return claims, nil
}

func asString(value any) string {
	s, _ := value.(string)
	return s
}

func jsonResponse(request *http.Request, body []byte) *http.Response {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode: http.StatusOK, Header: header,
		Body: io.NopCloser(bytes.NewReader(body)), Request: request,
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }
