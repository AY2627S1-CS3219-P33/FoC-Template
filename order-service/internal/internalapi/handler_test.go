package internalapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/auth/authtest"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/opsrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/httpapi"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/serviceauth"
)

const testUserID = "auth0|6f1c2b9e0d4a"

type fakeErrands struct {
	counts map[string]opsrepo.ActiveErrandCounts
	err    error
	calls  []string
}

func (f *fakeErrands) CountActiveErrandsForUser(_ context.Context, userID string) (opsrepo.ActiveErrandCounts, error) {
	f.calls = append(f.calls, userID)
	return f.counts[userID], f.err
}

type harness struct {
	router  http.Handler
	fixture *authtest.Fixture
	service string // a valid service token with orders:internal
	logs    *bytes.Buffer
}

func newHarness(t *testing.T, errands ActiveErrands) harness {
	t.Helper()
	f := authtest.New(t)
	cfg := auth.DefaultAuth0Config()
	cfg.Issuer = f.Server.URL + "/"
	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	verifier, err := auth.NewAuth0(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), auth.WithHTTPClient(f.Server.Client()))
	require.NoError(t, err)
	guard := serviceauth.NewMiddleware(auth.NewMiddleware(verifier))
	router := httpapi.NewRouter(httpapi.Dependencies{Logger: logger}, NewHandler(errands, guard, logger))
	token := f.Token(t, f.ServiceClaims(auth.OrderAudience, []string{string(auth.InternalAccess)}), "one")
	return harness{router: router, fixture: f, service: token, logs: logs}
}

func (h harness) get(t *testing.T, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	h.router.ServeHTTP(recorder, request)
	return recorder
}

func activePath(userID string) string {
	return "/internal/users/" + url.PathEscape(userID) + "/active-errands"
}

func decodeError(t *testing.T, recorder *httptest.ResponseRecorder) apperror.Error {
	t.Helper()
	require.Equal(t, "application/json", recorder.Header().Get("Content-Type"))
	var body apperror.Error
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	return body
}

func TestActiveErrandsReturnsCountsPerRole_F1_2_2_F1_2_3(t *testing.T) {
	errands := &fakeErrands{counts: map[string]opsrepo.ActiveErrandCounts{
		testUserID:          {AsRequester: 2, AsCourier: 1},
		"auth0|requester":   {AsRequester: 3},
		"auth0|courier":     {AsCourier: 1},
		"google-oauth2|123": {},
	}}
	h := newHarness(t, errands)

	tests := []struct {
		userID string
		want   string
	}{
		{testUserID, `{"userId":"` + testUserID + `","asRequester":2,"asCourier":1}`},
		{"auth0|requester", `{"userId":"auth0|requester","asRequester":3,"asCourier":0}`},
		{"auth0|courier", `{"userId":"auth0|courier","asRequester":0,"asCourier":1}`},
		{"google-oauth2|123", `{"userId":"google-oauth2|123","asRequester":0,"asCourier":0}`},
		{"auth0|unknown", `{"userId":"auth0|unknown","asRequester":0,"asCourier":0}`},
	}
	for _, tt := range tests {
		t.Run(tt.userID, func(t *testing.T) {
			errands.calls = nil
			recorder := h.get(t, activePath(tt.userID), h.service)

			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			require.Equal(t, "application/json", recorder.Header().Get("Content-Type"))
			require.JSONEq(t, tt.want, recorder.Body.String())
			require.Equal(t, []string{tt.userID}, errands.calls)
		})
	}
}

func TestActiveErrandsRequiresServiceCredential_NFR3_5_3(t *testing.T) {
	errands := &fakeErrands{}
	h := newHarness(t, errands)
	f := h.fixture
	internal := []string{string(auth.InternalAccess)}
	userToken := f.Token(t, f.Claims(auth.OrderAudience, internal), "one")
	noPermission := f.Token(t, f.ServiceClaims(auth.OrderAudience, []string{string(auth.UseErrands)}), "one")
	adminToken := f.Token(t, f.Claims(auth.OrderAudience, []string{string(auth.ReviewErrands)}), "one")

	tests := []struct {
		name   string
		token  string
		status int
		code   apperror.Code
	}{
		{"no token", "", http.StatusUnauthorized, apperror.Unauthenticated},
		{"invalid token", "not.a.jwt", http.StatusUnauthorized, apperror.Unauthenticated},
		{"user token with internal permission", userToken, http.StatusForbidden, apperror.Forbidden},
		{"admin user token", adminToken, http.StatusForbidden, apperror.Forbidden},
		{"service token without internal permission", noPermission, http.StatusForbidden, apperror.Forbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := h.get(t, activePath(testUserID), tt.token)
			require.Equal(t, tt.status, recorder.Code)
			require.Equal(t, tt.code, decodeError(t, recorder).Code)
		})
	}
	require.Empty(t, errands.calls, "a rejected credential never reaches the store")
}

func TestActiveErrandsRejectsOverlongUserID_NFR2_2_1(t *testing.T) {
	errands := &fakeErrands{}
	h := newHarness(t, errands)

	recorder := h.get(t, activePath(strings.Repeat("a", maxUserIDLength+1)), h.service)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Equal(t, apperror.InvalidArgument, decodeError(t, recorder).Code)

	recorder = h.get(t, "/internal/users/%FF/active-errands", h.service)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Equal(t, apperror.InvalidArgument, decodeError(t, recorder).Code)
	require.Empty(t, errands.calls)

	recorder = h.get(t, activePath(strings.Repeat("a", maxUserIDLength)), h.service)
	require.Equal(t, http.StatusOK, recorder.Code)
}

func TestActiveErrandsHidesStoreFailure(t *testing.T) {
	errands := &fakeErrands{err: errors.New("dial tcp postgres://order:secret@db:5432: refused")}
	h := newHarness(t, errands)

	recorder := h.get(t, activePath(testUserID), h.service)
	require.Equal(t, http.StatusInternalServerError, recorder.Code)
	body := decodeError(t, recorder)
	require.Equal(t, apperror.Internal, body.Code)
	require.NotContains(t, recorder.Body.String(), "secret")
	require.NotContains(t, recorder.Body.String(), "postgres://")
	require.Contains(t, h.logs.String(), "active errand count failed")
	require.Contains(t, h.logs.String(), "caller=fixture-client@clients")
}
