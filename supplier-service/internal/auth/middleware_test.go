package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/apperror"
	"github.com/stretchr/testify/require"
)

type fakePort struct {
	authenticateFunc func(ctx context.Context, token string) (Principal, error)
}

func (f *fakePort) Authenticate(ctx context.Context, token string) (Principal, error) {
	if f.authenticateFunc != nil {
		return f.authenticateFunc(ctx, token)
	}
	return Principal{}, &AuthenticationError{Kind: InvalidCredential}
}

func TestMiddlewarePanicsOnNilPort(t *testing.T) {
	require.Panics(t, func() {
		NewMiddleware(nil)
	})
}

func TestMiddlewareMissingAuthorizationHeader(t *testing.T) {
	middleware := NewMiddleware(&fakePort{})
	handler := middleware.RequireRead(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/suppliers", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	var errResp apperror.Error
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&errResp))
	require.Equal(t, apperror.Unauthenticated, errResp.Code)
	require.Contains(t, errResp.Message, "authorization header is required")
}

func TestMiddlewareMalformedAuthorizationHeader(t *testing.T) {
	middleware := NewMiddleware(&fakePort{})
	handler := middleware.RequireRead(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	cases := []struct {
		name   string
		header string
	}{
		{name: "basic auth scheme", header: "Basic dXNlcjpwYXNz"},
		{name: "missing token after bearer", header: "Bearer "},
		{name: "single word", header: "Bearer"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/suppliers", nil)
			req.Header.Set("Authorization", tc.header)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			require.Equal(t, http.StatusUnauthorized, rec.Code)
			var errResp apperror.Error
			require.NoError(t, json.NewDecoder(rec.Body).Decode(&errResp))
			require.Equal(t, apperror.Unauthenticated, errResp.Code)
		})
	}
}

func TestMiddlewareAuthenticationFailureKinds(t *testing.T) {
	tests := []struct {
		name           string
		failureKind    FailureKind
		expectedStatus int
		expectedCode   apperror.Code
	}{
		{
			name:           "invalid or expired credentials",
			failureKind:    InvalidCredential,
			expectedStatus: http.StatusUnauthorized,
			expectedCode:   apperror.Unauthenticated,
		},
		{
			name:           "account is disabled",
			failureKind:    AccountDisabled,
			expectedStatus: http.StatusForbidden,
			expectedCode:   apperror.Forbidden,
		},
		{
			name:           "verifier service unavailable",
			failureKind:    VerifierUnavailable,
			expectedStatus: http.StatusServiceUnavailable,
			expectedCode:   apperror.DependencyUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			port := &fakePort{
				authenticateFunc: func(ctx context.Context, token string) (Principal, error) {
					return Principal{}, &AuthenticationError{Kind: tt.failureKind}
				},
			}
			middleware := NewMiddleware(port)
			handler := middleware.RequireRead(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequest(http.MethodGet, "/suppliers", nil)
			req.Header.Set("Authorization", "Bearer valid-format-token")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			require.Equal(t, tt.expectedStatus, rec.Code)
			var errResp apperror.Error
			require.NoError(t, json.NewDecoder(rec.Body).Decode(&errResp))
			require.Equal(t, tt.expectedCode, errResp.Code)
		})
	}
}

func TestMiddlewareUnexpectedVerifierError(t *testing.T) {
	port := &fakePort{
		authenticateFunc: func(ctx context.Context, token string) (Principal, error) {
			return Principal{}, errors.New("raw internal db connection failure")
		},
	}
	middleware := NewMiddleware(port)
	handler := middleware.RequireRead(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/suppliers", nil)
	req.Header.Set("Authorization", "Bearer token-123")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	var errResp apperror.Error
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&errResp))
	require.Equal(t, apperror.Internal, errResp.Code)
	require.NotContains(t, errResp.Message, "raw internal db")
}

func TestMiddlewarePermissionsAndContextInjection(t *testing.T) {
	userPrincipal := Principal{
		Subject: "user-42",
		Roles:   []Role{RoleUser},
	}
	adminPrincipal := Principal{
		Subject: "admin-99",
		Roles:   []Role{RoleAdministrator},
	}

	t.Run("normal user can access read endpoint and context receives principal", func(t *testing.T) {
		port := &fakePort{
			authenticateFunc: func(ctx context.Context, token string) (Principal, error) {
				return userPrincipal, nil
			},
		}
		middleware := NewMiddleware(port)

		var capturedPrincipal Principal
		var principalFound bool
		handler := middleware.RequireRead(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedPrincipal, principalFound = PrincipalFromContext(r.Context())
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest(http.MethodGet, "/suppliers?role=administrator", nil)
		req.Header.Set("Authorization", "Bearer user-token")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		require.True(t, principalFound)
		require.Equal(t, userPrincipal, capturedPrincipal)
	})

	t.Run("normal user is rejected from admin endpoint", func(t *testing.T) {
		port := &fakePort{
			authenticateFunc: func(ctx context.Context, token string) (Principal, error) {
				return userPrincipal, nil
			},
		}
		middleware := NewMiddleware(port)
		handler := middleware.RequireManage(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
		}))

		req := httptest.NewRequest(http.MethodPost, "/suppliers", nil)
		req.Header.Set("Authorization", "Bearer user-token")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		require.Equal(t, http.StatusForbidden, rec.Code)
		var errResp apperror.Error
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&errResp))
		require.Equal(t, apperror.Forbidden, errResp.Code)
	})

	t.Run("admin can access manage endpoint", func(t *testing.T) {
		port := &fakePort{
			authenticateFunc: func(ctx context.Context, token string) (Principal, error) {
				return adminPrincipal, nil
			},
		}
		middleware := NewMiddleware(port)

		var capturedPrincipal Principal
		handler := middleware.RequireManage(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedPrincipal, _ = PrincipalFromContext(r.Context())
			w.WriteHeader(http.StatusCreated)
		}))

		req := httptest.NewRequest(http.MethodPost, "/suppliers", nil)
		req.Header.Set("Authorization", "Bearer admin-token")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		require.Equal(t, http.StatusCreated, rec.Code)
		require.Equal(t, adminPrincipal, capturedPrincipal)
	})
}
