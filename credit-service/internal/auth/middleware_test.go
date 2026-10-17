package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

type portFunc func(context.Context, string) (Principal, error)

func (f portFunc) Authenticate(ctx context.Context, token string) (Principal, error) {
	return f(ctx, token)
}

func call(port Port, caller Caller, permission Permission, authorization string) (*httptest.ResponseRecorder, bool) {
	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true })
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	response := httptest.NewRecorder()
	NewMiddleware(port).Require(caller, permission)(next).ServeHTTP(response, request)
	return response, reached
}

func TestRequireRejectsBadAuthorizationHeaders(t *testing.T) {
	port := portFunc(func(context.Context, string) (Principal, error) { panic("must not be called") })
	for name, header := range map[string]string{
		"missing":      "",
		"basic scheme": "Basic abc",
		"one word":     "Bearer",
		"blank token":  "Bearer    ",
	} {
		response, reached := call(port, User, ReadOwnWallet, header)
		require.Equal(t, http.StatusUnauthorized, response.Code, name)
		require.False(t, reached, name)
	}
}

func TestRequireMapsVerifierOutcomes(t *testing.T) {
	for name, c := range map[string]struct {
		err    error
		status int
	}{
		"invalid":     {&AuthenticationError{Kind: InvalidCredential}, http.StatusUnauthorized},
		"unavailable": {&AuthenticationError{Kind: VerifierUnavailable}, http.StatusServiceUnavailable},
		"unexpected":  {errors.New("secret detail"), http.StatusInternalServerError},
	} {
		port := portFunc(func(context.Context, string) (Principal, error) { return Principal{}, c.err })
		response, reached := call(port, User, ReadOwnWallet, "Bearer t")
		require.Equal(t, c.status, response.Code, name)
		require.False(t, reached, name)
		require.NotContains(t, response.Body.String(), "secret detail", name)
	}
}

func TestRequireChecksCallerKindAndPermission(t *testing.T) {
	user := Principal{Subject: "auth0|a", Permissions: []Permission{ReadOwnWallet}}
	service := Principal{Subject: "c@clients", Machine: true, Permissions: []Permission{ReadWallet}}

	for name, c := range map[string]struct {
		principal  Principal
		caller     Caller
		permission Permission
		allowed    bool
	}{
		"user on user route":             {user, User, ReadOwnWallet, true},
		"service on service route":       {service, Service, ReadWallet, true},
		"user on service route":          {user, Service, ReadOwnWallet, false},
		"service on user route":          {service, User, ReadWallet, false},
		"missing permission":             {user, User, ReadWallet, false},
		"service without the permission": {service, Service, CreateWallet, false},
	} {
		port := portFunc(func(context.Context, string) (Principal, error) { return c.principal, nil })
		response, reached := call(port, c.caller, c.permission, "Bearer t")
		require.Equal(t, c.allowed, reached, name)
		if !c.allowed {
			require.Equal(t, http.StatusForbidden, response.Code, name)
		}
	}
}

func TestRequireStoresPrincipalInContext(t *testing.T) {
	want := Principal{Subject: "auth0|a", Permissions: []Permission{ReadOwnWallet}}
	port := portFunc(func(context.Context, string) (Principal, error) { return want, nil })
	var got Principal
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got, _ = PrincipalFromContext(r.Context()) })
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer t")
	NewMiddleware(port).Require(User, ReadOwnWallet)(next).ServeHTTP(httptest.NewRecorder(), request)
	require.Equal(t, want, got)
}
