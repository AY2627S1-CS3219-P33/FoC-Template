package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/auth0/go-jwt-middleware/v3/core"
	"github.com/auth0/go-jwt-middleware/v3/validator"

	"user-service/internal/middleware"
	"user-service/internal/repository"
	"user-service/internal/service"
)

type provisionerStub struct {
	profile        *service.Profile
	created        bool
	err            error
	token          string
	subject        string
	accountID      string
	authorizeErr   error
	authorizeCalls int
}

func (p *provisionerStub) Provision(_ context.Context, subject, token string) (*service.Profile, bool, error) {
	p.subject, p.token = subject, token
	return p.profile, p.created, p.err
}

func (p *provisionerStub) RequireActiveAccount(_ context.Context, subject string) (string, error) {
	p.authorizeCalls++
	p.subject = subject
	return p.accountID, p.authorizeErr
}

func testAuthentication(t *testing.T) *middleware.Auth0 {
	t.Helper()
	authentication, err := middleware.NewAuth0("example.auth0.com", "https://api.example.com")
	if err != nil {
		t.Fatal(err)
	}
	return authentication
}

func TestPublicRoutesAndProtectedRejection(t *testing.T) {
	h := New(AuthConfig{Domain: "example.auth0.com", ClientID: "client", Audience: "audience"}, testAuthentication(t), &provisionerStub{}, service.NewUserService(nil, nil))
	tests := []struct {
		method     string
		path       string
		wantStatus int
		contains   string
	}{
		{path: "/", wantStatus: http.StatusOK, contains: "Auth0 login test"},
		{path: "/assets/app.js", wantStatus: http.StatusOK, contains: "loginWithRedirect"},
		{path: "/api/auth/config", wantStatus: http.StatusOK, contains: `"clientId":"client"`},
		{path: "/health", wantStatus: http.StatusNoContent},
		{path: "/missing", wantStatus: http.StatusNotFound},
		{path: "/api/me", wantStatus: http.StatusUnauthorized, contains: "missing_token"},
		{method: http.MethodPatch, path: "/api/me", wantStatus: http.StatusUnauthorized, contains: "missing_token"},
		{path: "/api/private", wantStatus: http.StatusUnauthorized, contains: "missing_token"},
		{method: http.MethodPost, path: "/api/auth/logout", wantStatus: http.StatusUnauthorized, contains: "missing_token"},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			method := test.method
			if method == "" {
				method = http.MethodGet
			}
			h.Router.ServeHTTP(response, httptest.NewRequest(method, test.path, nil))
			if response.Code != test.wantStatus || (test.contains != "" && !strings.Contains(response.Body.String(), test.contains)) {
				t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
			}
		})
	}
}

func TestLogoutAcceptsValidatedSubject(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	claims := &validator.ValidatedClaims{RegisteredClaims: validator.RegisteredClaims{Subject: "auth0|student"}}
	request = request.WithContext(core.SetClaims(request.Context(), claims))
	response := httptest.NewRecorder()
	logout(response, request)
	if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestProvisionUsesValidatedSubjectAndBearerToken(t *testing.T) {
	stub := &provisionerStub{
		profile: &service.Profile{ID: "account-id", Username: "student"}, created: true,
		authorizeErr: service.ErrNotFound,
	}
	request := httptest.NewRequest(http.MethodPost, "/api/auth/provision", nil)
	request.Header.Set("Authorization", "Bearer validated-token")
	claims := &validator.ValidatedClaims{RegisteredClaims: validator.RegisteredClaims{Subject: "auth0|student"}}
	request = request.WithContext(core.SetClaims(request.Context(), claims))
	response := httptest.NewRecorder()
	provision(response, request, stub)
	if response.Code != http.StatusCreated || stub.subject != "auth0|student" || stub.token != "validated-token" || stub.authorizeCalls != 0 {
		t.Fatalf("status=%d subject=%q token=%q authorization_calls=%d", response.Code, stub.subject, stub.token, stub.authorizeCalls)
	}
}

func TestRequireActiveAccount(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		want     int
		contains string
	}{
		{"active", nil, http.StatusNoContent, ""},
		{"not provisioned", service.ErrNotFound, http.StatusForbidden, "account_not_provisioned"},
		{"inactive", service.ErrInactive, http.StatusForbidden, "account_inactive"},
		{"unavailable", service.ErrUnavailable, http.StatusServiceUnavailable, "account_check_unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stub := &provisionerStub{accountID: "account-id", authorizeErr: test.err}
			request := httptest.NewRequest(http.MethodGet, "/api/private", nil)
			claims := &validator.ValidatedClaims{RegisteredClaims: validator.RegisteredClaims{Subject: "auth0|student"}}
			request = request.WithContext(core.SetClaims(request.Context(), claims))
			response := httptest.NewRecorder()
			requireActiveAccount(stub, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			})).ServeHTTP(response, request)
			if response.Code != test.want || (test.contains != "" && !strings.Contains(response.Body.String(), test.contains)) {
				t.Fatalf("status=%d body=%q want=%d/%q", response.Code, response.Body.String(), test.want, test.contains)
			}
			if stub.authorizeCalls != 1 || stub.subject != "auth0|student" {
				t.Fatalf("authorization_calls=%d subject=%q", stub.authorizeCalls, stub.subject)
			}
		})
	}
}

func TestProvisionErrorMapping(t *testing.T) {
	tests := []struct {
		err      error
		want     int
		contains string
	}{
		{service.ErrIdentityUnverified, http.StatusForbidden, "email_not_verified"},
		{service.ErrIdentityIneligible, http.StatusForbidden, "ineligible_email"},
		{service.ErrIdentityMismatch, http.StatusUnauthorized, "identity_mismatch"},
		{service.ErrInactive, http.StatusForbidden, "account_inactive"},
		{repository.ErrConflict, http.StatusConflict, "account_conflict"},
		{service.ErrProfileUnavailable, http.StatusBadGateway, "auth0_profile_unavailable"},
		{service.ErrUnavailable, http.StatusServiceUnavailable, "provisioning_unavailable"},
		{errors.New("unexpected failure"), http.StatusInternalServerError, "internal_error"},
	}
	for _, test := range tests {
		response := httptest.NewRecorder()
		writeProvisionError(response, test.err)
		if response.Code != test.want || !strings.Contains(response.Body.String(), test.contains) {
			t.Fatalf("err=%v status=%d body=%q want=%d/%q", test.err, response.Code, response.Body.String(), test.want, test.contains)
		}
	}
}
