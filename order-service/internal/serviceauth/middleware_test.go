package serviceauth

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/auth/authtest"
	"github.com/stretchr/testify/require"
)

func serviceMiddleware(t *testing.T) (*Middleware, *authtest.Fixture) {
	t.Helper()
	f := authtest.New(t)
	cfg := auth.DefaultAuth0Config()
	cfg.Issuer = f.Server.URL + "/"
	verifier, err := auth.NewAuth0(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), auth.WithHTTPClient(f.Server.Client()))
	require.NoError(t, err)
	return NewMiddleware(auth.NewMiddleware(verifier)), f
}

func TestRequireServiceAllowsServiceTokenWithPermission_NFR3_5_3(t *testing.T) {
	m, f := serviceMiddleware(t)
	token := f.Token(t, f.ServiceClaims(auth.OrderAudience, []string{string(auth.InternalAccess)}), "one")

	var got auth.Principal
	var found bool
	handler := m.RequireService(auth.InternalAccess)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, found = auth.PrincipalFromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPost, "/internal/fence", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNoContent, rec.Code)
	require.True(t, found)
	require.True(t, got.IsService())
	require.Equal(t, "fixture-client@clients", got.Subject)
	require.True(t, got.Has(auth.InternalAccess))
}

func TestRequireServiceRejects_NFR3_5_3(t *testing.T) {
	m, f := serviceMiddleware(t)
	internal := []string{string(auth.InternalAccess)}
	expired := f.ServiceClaims(auth.OrderAudience, internal)
	expired["exp"] = time.Now().Add(-time.Hour).Unix()

	tests := []struct {
		name       string
		header     string
		permission auth.Permission
		status     int
		code       apperror.Code
	}{
		{name: "no header", header: "", status: http.StatusUnauthorized, code: apperror.Unauthenticated},
		{name: "not bearer", header: "Basic abc", status: http.StatusUnauthorized, code: apperror.Unauthenticated},
		{name: "garbage token", header: "Bearer not.a.jwt", status: http.StatusUnauthorized, code: apperror.Unauthenticated},
		{name: "expired service token", header: "Bearer " + f.Token(t, expired, "one"), status: http.StatusUnauthorized, code: apperror.Unauthenticated},
		{name: "service token for another audience", header: "Bearer " + f.Token(t, f.ServiceClaims("https://api.foc.local/other", internal), "one"), status: http.StatusUnauthorized, code: apperror.Unauthenticated},
		{name: "user token with internal permission", header: "Bearer " + f.Token(t, f.Claims(auth.OrderAudience, internal), "one"), status: http.StatusForbidden, code: apperror.Forbidden},
		{name: "service token without permissions", header: "Bearer " + f.Token(t, f.ServiceClaims(auth.OrderAudience, nil), "one"), status: http.StatusForbidden, code: apperror.Forbidden},
		{name: "service token with other permission", header: "Bearer " + f.Token(t, f.ServiceClaims(auth.OrderAudience, []string{string(auth.UseErrands)}), "one"), status: http.StatusForbidden, code: apperror.Forbidden},
		{name: "unknown permission is never granted", header: "Bearer " + f.Token(t, f.ServiceClaims(auth.OrderAudience, []string{"bogus:perm"}), "one"), permission: "bogus:perm", status: http.StatusForbidden, code: apperror.Forbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			permission := tt.permission
			if permission == "" {
				permission = auth.InternalAccess
			}
			handler := m.RequireService(permission)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("rejected request reached an internal route")
			}))
			req := httptest.NewRequest(http.MethodPost, "/internal/fence", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			require.Equal(t, tt.status, rec.Code)
			var body apperror.Error
			require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
			require.Equal(t, tt.code, body.Code)
		})
	}
}

func TestNewMiddlewareRequiresAuth(t *testing.T) {
	require.Panics(t, func() { NewMiddleware(nil) })
}
