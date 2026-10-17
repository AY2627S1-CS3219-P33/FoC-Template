package auth

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/testauth"
)

func verifierFixture(t *testing.T) (*Auth0, *testauth.Fixture) {
	t.Helper()
	f := testauth.New(t)
	cfg := DefaultAuth0Config()
	cfg.Issuer = f.Server.URL + "/"
	cfg.ClockSkew = 0
	verifier, err := newAuth0(cfg, f.Server.Client(), time.Now, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	return verifier, f
}

func requireKind(t *testing.T, err error, kind FailureKind) {
	t.Helper()
	var failure *AuthenticationError
	require.ErrorAs(t, err, &failure)
	require.Equal(t, kind, failure.Kind)
	require.Equal(t, "authentication failed", err.Error())
}

func TestVerifierAcceptsUserToken(t *testing.T) {
	v, f := verifierFixture(t)
	claims := f.Claims(CreditAudience, []string{"credits:read:own"})

	principal, err := v.Authenticate(context.Background(), f.Token(t, claims, "one"))
	require.NoError(t, err)
	require.Equal(t, "auth0|fixture-user", principal.Subject)
	require.False(t, principal.Machine)
	require.True(t, principal.Has(ReadOwnWallet))
	require.False(t, principal.Has(ReadWallet))
}

func TestVerifierMarksClientCredentialsTokensAsMachine(t *testing.T) {
	v, f := verifierFixture(t)
	claims := f.Claims(CreditAudience, []string{"credits:wallets:create", "credits:read"})
	claims["sub"] = "abc123@clients"
	claims["gty"] = "client-credentials"

	principal, err := v.Authenticate(context.Background(), f.Token(t, claims, "one"))
	require.NoError(t, err)
	require.True(t, principal.Machine)
	require.True(t, principal.Has(CreateWallet))
	require.True(t, principal.Has(ReadWallet))
}

func TestVerifierRejectsInvalidTokens(t *testing.T) {
	v, f := verifierFixture(t)
	good := func() map[string]any { return f.Claims(CreditAudience, []string{"credits:read:own"}) }
	with := func(change func(map[string]any)) map[string]any {
		claims := good()
		change(claims)
		return claims
	}

	tests := map[string]string{
		"wrong audience": f.Token(t, with(func(c map[string]any) { c["aud"] = "https://api.foc.local/supplier-service" }), "one"),
		"wrong issuer":   f.Token(t, with(func(c map[string]any) { c["iss"] = "https://evil.example/" }), "one"),
		"expired":        f.Token(t, with(func(c map[string]any) { c["exp"] = time.Now().Add(-time.Minute).Unix() }), "one"),
		"no subject":     f.Token(t, with(func(c map[string]any) { delete(c, "sub") }), "one"),
		"unknown key id": f.Token(t, good(), "ghost"),
		"no key id":      f.Token(t, good(), ""),
		"symmetric alg":  testauth.Sign(t, good(), "one", []byte("shared-secret-shared-secret-1234"), jwa.HS256()),
		"garbage":        "not.a.jwt",
		"empty":          "",
	}
	for name, token := range tests {
		_, err := v.Authenticate(context.Background(), token)
		requireKind(t, err, InvalidCredential)
		_ = name
	}
}

func TestVerifierReportsUnavailableWhenKeysCannotBeFetched(t *testing.T) {
	v, f := verifierFixture(t)
	f.Response(500, nil, 0, "")
	token := f.Token(t, f.Claims(CreditAudience, []string{"credits:read:own"}), "one")

	_, err := v.Authenticate(context.Background(), token)
	requireKind(t, err, VerifierUnavailable)
}

func TestPrincipalHasIgnoresUnknownPermissionsAndAnonymousCallers(t *testing.T) {
	require.False(t, Principal{Subject: "s", Permissions: []Permission{"credits:everything"}}.Has("credits:everything"))
	require.False(t, Principal{Permissions: []Permission{ReadWallet}}.Has(ReadWallet), "no subject, no access")
}
