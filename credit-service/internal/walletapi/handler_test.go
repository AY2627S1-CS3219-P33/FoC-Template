package walletapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/wallet"
)

const (
	owner   = "00000000-0000-0000-0000-00000000a11c"
	subject = "auth0|alice"
)

type portStub map[string]auth.Principal

func (p portStub) Authenticate(_ context.Context, token string) (auth.Principal, error) {
	if principal, ok := p[token]; ok {
		return principal, nil
	}
	return auth.Principal{}, &auth.AuthenticationError{Kind: auth.InvalidCredential}
}

var tokens = portStub{
	"user":         {Subject: subject, Permissions: []auth.Permission{auth.ReadOwnWallet}},
	"other-user":   {Subject: "auth0|bob", Permissions: []auth.Permission{auth.ReadOwnWallet}},
	"user-no-read": {Subject: subject},
	// A user token that somehow carries service permissions must still be refused.
	"user-with-service-perms": {Subject: subject, Permissions: []auth.Permission{auth.ReadWallet, auth.CreateWallet}},
	"reader":                  {Subject: "client@clients", Machine: true, Permissions: []auth.Permission{auth.ReadWallet}},
	"creator":                 {Subject: "client@clients", Machine: true, Permissions: []auth.Permission{auth.CreateWallet}},
	// A machine token has no wallet of its own, even with the user permission.
	"machine-own": {Subject: "client@clients", Machine: true, Permissions: []auth.Permission{auth.ReadOwnWallet}},
}

type serviceStub struct {
	created   bool
	err       error
	gotOwner  string
	gotSub    string
	available int64
}

func (s *serviceStub) result(ownerID, sub string) wallet.Wallet {
	return wallet.Wallet{OwnerID: ownerID, Auth0Subject: sub, Available: s.available, Reserved: 5,
		CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
}
func (s *serviceStub) CreateWallet(_ context.Context, ownerID, sub string) (wallet.Wallet, bool, error) {
	s.gotOwner, s.gotSub = ownerID, sub
	return s.result(ownerID, sub), s.created, s.err
}
func (s *serviceStub) GetByOwner(_ context.Context, ownerID string) (wallet.Wallet, error) {
	s.gotOwner = ownerID
	return s.result(ownerID, subject), s.err
}
func (s *serviceStub) GetBySubject(_ context.Context, sub string) (wallet.Wallet, error) {
	s.gotSub = sub
	return s.result(owner, sub), s.err
}

func serve(svc *serviceStub, method, path, token, body string) *httptest.ResponseRecorder {
	router := http.NewServeMux()
	NewHandler(svc, auth.NewMiddleware(tokens), nil).RegisterRoutes(router)
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestUserReadsOwnWalletFromTokenSubject(t *testing.T) {
	svc := &serviceStub{available: 100}
	// Anything the caller puts in the query string is ignored.
	response := serve(svc, http.MethodGet, "/v1/wallet/me?ownerId=someone-else&sub=auth0|bob", "user", "")
	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, subject, svc.gotSub)
	require.JSONEq(t, `{"ownerId":"`+owner+`","available":"100","reserved":"5","createdAt":"2026-01-02T03:04:05Z"}`,
		response.Body.String())

	svc = &serviceStub{}
	serve(svc, http.MethodGet, "/v1/wallet/me", "other-user", "")
	require.Equal(t, "auth0|bob", svc.gotSub, "each user reads only the wallet of their own subject")
}

func TestUserWithoutWalletGetsNotFound(t *testing.T) {
	response := serve(&serviceStub{err: wallet.ErrNotFound}, http.MethodGet, "/v1/wallet/me", "user", "")
	require.Equal(t, http.StatusNotFound, response.Code)
	require.JSONEq(t, `{"code":"WALLET_NOT_FOUND","message":"wallet not found"}`, response.Body.String())
}

func TestUsersCannotCallServiceRoutes(t *testing.T) {
	for _, token := range []string{"user", "user-with-service-perms"} {
		svc := &serviceStub{}
		for _, route := range []struct{ method, body string }{{http.MethodGet, ""}, {http.MethodPut, `{"auth0Subject":"auth0|x"}`}} {
			response := serve(svc, route.method, "/internal/wallets/"+owner, token, route.body)
			require.Equal(t, http.StatusForbidden, response.Code, token+" "+route.method)
		}
		require.Empty(t, svc.gotOwner, "the service must not be reached")
	}
}

func TestMachineTokensCannotUseUserRoute(t *testing.T) {
	svc := &serviceStub{}
	response := serve(svc, http.MethodGet, "/v1/wallet/me", "machine-own", "")
	require.Equal(t, http.StatusForbidden, response.Code)
	require.Empty(t, svc.gotSub)
}

func TestServicePermissionsAreSeparate(t *testing.T) {
	svc := &serviceStub{}
	require.Equal(t, http.StatusForbidden, serve(svc, http.MethodPut, "/internal/wallets/"+owner, "reader", `{"auth0Subject":"x"}`).Code)
	require.Equal(t, http.StatusForbidden, serve(svc, http.MethodGet, "/internal/wallets/"+owner, "creator", "").Code)
	require.Equal(t, http.StatusForbidden, serve(svc, http.MethodGet, "/v1/wallet/me", "user-no-read", "").Code)
	require.Equal(t, http.StatusOK, serve(svc, http.MethodGet, "/internal/wallets/"+owner, "reader", "").Code)
}

func TestCreateReportsCreatedThenExisting(t *testing.T) {
	svc := &serviceStub{created: true, available: 100}
	response := serve(svc, http.MethodPut, "/internal/wallets/"+owner, "creator", `{"auth0Subject":"`+subject+`"}`)
	require.Equal(t, http.StatusCreated, response.Code)
	require.Equal(t, owner, svc.gotOwner)
	require.Equal(t, subject, svc.gotSub)

	var body map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Equal(t, "100", body["available"], "balances are strings")
	require.NotContains(t, body, "auth0Subject")

	svc.created = false
	response = serve(svc, http.MethodPut, "/internal/wallets/"+owner, "creator", `{"auth0Subject":"`+subject+`"}`)
	require.Equal(t, http.StatusOK, response.Code)
}

func TestCreateRejectsBadBodies(t *testing.T) {
	for name, body := range map[string]string{
		"empty":         ``,
		"not json":      `nope`,
		"unknown field": `{"auth0Subject":"a","initialCredit":1000000}`,
		"two objects":   `{"auth0Subject":"a"}{"auth0Subject":"b"}`,
		"too large":     `{"auth0Subject":"` + strings.Repeat("a", 5000) + `"}`,
	} {
		svc := &serviceStub{}
		response := serve(svc, http.MethodPut, "/internal/wallets/"+owner, "creator", body)
		require.Equal(t, http.StatusBadRequest, response.Code, name)
		require.Empty(t, svc.gotOwner, name)
	}
}

func TestErrorMapping(t *testing.T) {
	for name, c := range map[string]struct {
		err    error
		status int
		code   string
	}{
		"invalid":  {errors.Join(wallet.ErrInvalid), http.StatusBadRequest, "INVALID_ARGUMENT"},
		"conflict": {wallet.ErrConflict, http.StatusConflict, "WALLET_CONFLICT"},
		"internal": {errors.New("password=hunter2 refused"), http.StatusInternalServerError, "INTERNAL"},
	} {
		response := serve(&serviceStub{err: c.err}, http.MethodPut, "/internal/wallets/"+owner, "creator", `{"auth0Subject":"a"}`)
		require.Equal(t, c.status, response.Code, name)
		require.Contains(t, response.Body.String(), c.code, name)
		require.NotContains(t, response.Body.String(), "hunter2", name)
	}
}
