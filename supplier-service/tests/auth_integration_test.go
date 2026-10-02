package tests

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/app"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/testauth"
	"github.com/stretchr/testify/require"
)

type authRouteStore struct {
	supplier.DeletionStore                   // Unexpected deletion calls panic in these focused tests.
	reads, writes, deletionRequests, retries int
}

func (s *authRouteStore) ListAvailable(context.Context, supplier.ListFilter) (supplier.Page, error) {
	s.reads++
	return supplier.Page{}, nil
}
func (s *authRouteStore) GetCurrentAvailable(context.Context, supplier.SupplierID) (supplier.Supplier, error) {
	s.reads++
	return supplier.Supplier{}, nil
}
func (s *authRouteStore) GetVersion(context.Context, supplier.VersionID) (supplier.Version, error) {
	s.reads++
	return supplier.Version{}, nil
}
func (s *authRouteStore) Create(_ context.Context, d supplier.Details, now time.Time) (supplier.Supplier, error) {
	s.writes++
	return supplier.Supplier{SupplierID: "00000000-0000-4000-8000-000000000001", VersionID: "00000000-0000-4000-8000-000000000002", Details: d, Available: true, CreatedAt: now, UpdatedAt: now}, nil
}
func (s *authRouteStore) Update(context.Context, supplier.SupplierID, supplier.Patch, time.Time) (supplier.Supplier, error) {
	s.writes++
	return supplier.Supplier{}, nil
}

func (s *authRouteStore) CreateOrGetDeletion(_ context.Context, op supplier.DeletionOperationID, id supplier.SupplierID, max int, now time.Time) (supplier.DeletionRecord, error) {
	s.deletionRequests++
	return supplier.DeletionRecord{OperationID: op, SupplierID: id, State: supplier.DeletionRequested, MaxAttempts: max}, nil
}

func (s *authRouteStore) ClaimDeletion(context.Context, supplier.DeletionOperationID, supplier.DeletionClaimID, time.Time, time.Time) (supplier.DeletionRecord, bool, error) {
	return supplier.DeletionRecord{State: supplier.DeletionRequested, AttemptCount: 1, MaxAttempts: 5}, true, nil
}

func (s *authRouteStore) ScheduleDeletionRetry(context.Context, supplier.DeletionOperationID, supplier.DeletionClaimID, supplier.DeletionFailureCode, *time.Time, time.Time) error {
	s.retries++
	return nil
}

type readyFunc func(context.Context) error

func (f readyFunc) Check(ctx context.Context) error { return f(ctx) }

const createBody = `{"name":"Fixture supplier","type":"food","building":"COM1","floor":"1","locationDescription":"Lobby","latitude":1.3,"longitude":103.7,"openingTime":"09:00","closingTime":"18:00"}`

func TestComposedAuth0Routes_F2_1_F2_2_NFR3_3(t *testing.T) {
	f := testauth.New(t)
	cfg := auth.DefaultAuth0Config()
	cfg.Issuer = f.Server.URL + "/"
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	verifier, err := auth.NewAuth0(cfg, logger, auth.WithHTTPClient(f.Server.Client()))
	require.NoError(t, err)
	store := &authRouteStore{}
	handler := app.NewHandler(logger, verifier, readyFunc(func(context.Context) error { return nil }), store, store, store, app.UnavailableDeletionFence{})
	read := f.Token(t, f.Claims(auth.SupplierAudience, []string{"suppliers:read"}), "one")
	admin := f.Token(t, f.Claims(auth.SupplierAudience, []string{"suppliers:read", "suppliers:manage"}), "one")
	noPermissions := f.Token(t, f.Claims(auth.SupplierAudience, nil), "one")
	manageOnly := f.Token(t, f.Claims(auth.SupplierAudience, []string{"suppliers:manage"}), "one")
	wrongAudience := f.Token(t, f.Claims("https://api.foc.local/user-service", []string{"suppliers:read"}), "one")
	for _, tt := range []struct {
		name, method, path, token, body string
		status                          int
	}{
		{"public readiness", "GET", "/readyz", "", "", 200},
		{"list needs login", "GET", "/suppliers", "", "", 401},
		{"version needs login", "GET", "/supplier-versions/00000000-0000-4000-8000-000000000002", "", "", 401},
		{"wrong audience", "GET", "/suppliers", wrongAudience, "", 401},
		{"missing permissions", "GET", "/suppliers", noPermissions, "", 403},
		{"read", "GET", "/suppliers", read, "", 200},
		{"detail", "GET", "/suppliers/00000000-0000-4000-8000-000000000001", read, "", 200},
		{"version", "GET", "/supplier-versions/00000000-0000-4000-8000-000000000002", read, "", 200},
		{"read cannot write", "POST", "/suppliers?role=administrator", read, createBody, 403},
		{"manage also needs router read permission", "POST", "/suppliers", manageOnly, createBody, 403},
		{"admin creates", "POST", "/suppliers", admin, createBody, 201},
		{"update needs login", "PATCH", "/suppliers/00000000-0000-4000-8000-000000000001", "", `{"name":"Updated"}`, 401},
		{"read cannot update", "PATCH", "/suppliers/00000000-0000-4000-8000-000000000001", read, `{"name":"Updated"}`, 403},
		{"admin updates", "PATCH", "/suppliers/00000000-0000-4000-8000-000000000001", admin, `{"name":"Updated"}`, 200},
		{"empty update rejected", "PATCH", "/suppliers/00000000-0000-4000-8000-000000000001", admin, `{}`, 400},
		{"delete needs login", "DELETE", "/suppliers/00000000-0000-4000-8000-000000000001", "", "", 401},
		{"read cannot delete", "DELETE", "/suppliers/00000000-0000-4000-8000-000000000001", read, "", 403},
		{"admin delete fails closed without fence", "DELETE", "/suppliers/00000000-0000-4000-8000-000000000001", admin, "", 503},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			if tt.token != "" {
				req.Header.Set("Authorization", "Bearer "+tt.token)
			}
			req.Header.Set("X-Role", "administrator")
			req.Header.Set("Idempotency-Key", "00000000-0000-4000-8000-000000000003")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			require.Equal(t, tt.status, rec.Code, rec.Body.String())
			if tt.method == "DELETE" && tt.status == 503 {
				require.Contains(t, rec.Body.String(), "DELETION_FENCE_UNAVAILABLE")
			}
			if tt.token != "" {
				require.NotContains(t, rec.Body.String(), tt.token)
			}
		})
	}
	require.Equal(t, 3, store.reads)
	require.Equal(t, 2, store.writes)
	require.Equal(t, 1, store.deletionRequests)
	require.Equal(t, 1, store.retries)
	require.EqualValues(t, 1, f.Hits.Load())

	// A fresh verifier cannot authorize through an unavailable key endpoint.
	f.Response(503, []byte("private-provider-error"), 0, "")
	fresh, err := auth.NewAuth0(cfg, logger, auth.WithHTTPClient(f.Server.Client()))
	require.NoError(t, err)
	unavailable := app.NewHandler(logger, fresh, readyFunc(fresh.Check), store, store, store, app.UnavailableDeletionFence{})
	req := httptest.NewRequest("GET", "/suppliers", nil)
	req.Header.Set("Authorization", "Bearer "+read)
	rec := httptest.NewRecorder()
	unavailable.ServeHTTP(rec, req)
	require.Equal(t, 503, rec.Code)
	require.Contains(t, rec.Body.String(), "DEPENDENCY_UNAVAILABLE")
	require.NotContains(t, rec.Body.String(), "private-provider-error")
	rec = httptest.NewRecorder()
	unavailable.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	require.Equal(t, 503, rec.Code)
	require.Contains(t, rec.Body.String(), "not_ready")
	require.Equal(t, 3, store.reads)
}

// Opt-in tenant acceptance. Real credentials are read only from the environment;
// assertion messages never print them or the decoded principal.
func TestAuth0TenantSmoke_C1(t *testing.T) {
	if os.Getenv("RUN_AUTH0_SMOKE_TEST") != "1" {
		t.Skip("set RUN_AUTH0_SMOKE_TEST=1 and dedicated test tokens to verify a real tenant")
	}
	cfg := auth.DefaultAuth0Config()
	cfg.Issuer = os.Getenv("AUTH0_ISSUER")
	v, err := auth.NewAuth0(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	for _, name := range []string{"AUTH0_SUPPLIER_READ_TOKEN", "AUTH0_SUPPLIER_ADMIN_TOKEN", "AUTH0_USER_SERVICE_TOKEN"} {
		require.NotEmpty(t, os.Getenv(name), name+" is required")
	}
	p, err := v.Authenticate(context.Background(), os.Getenv("AUTH0_SUPPLIER_READ_TOKEN"))
	require.NoError(t, err)
	require.True(t, p.Has(auth.ReadSuppliers))
	require.False(t, p.Has(auth.ManageSuppliers))
	p, err = v.Authenticate(context.Background(), os.Getenv("AUTH0_SUPPLIER_ADMIN_TOKEN"))
	require.NoError(t, err)
	require.True(t, p.Has(auth.ReadSuppliers))
	require.True(t, p.Has(auth.ManageSuppliers))
	_, err = v.Authenticate(context.Background(), os.Getenv("AUTH0_USER_SERVICE_TOKEN"))
	var failure *auth.AuthenticationError
	require.ErrorAs(t, err, &failure)
	require.Equal(t, auth.InvalidCredential, failure.Kind)
}
