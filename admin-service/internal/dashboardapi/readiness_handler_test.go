package dashboardapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/auth/authtest"
	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/httpapi"
	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/servicehealth"
)

type fakeReadiness struct {
	report servicehealth.Report
	calls  int
}

func (f *fakeReadiness) Check(context.Context) servicehealth.Report {
	f.calls++
	return f.report
}

func newReadinessHarness(t *testing.T, report servicehealth.Report) (http.Handler, *authtest.Fixture, *fakeReadiness) {
	t.Helper()
	f := authtest.New(t)
	cfg := auth.DefaultAuth0Config()
	cfg.Issuer = f.Server.URL + "/"
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	verifier, err := auth.NewAuth0(cfg, logger, auth.WithHTTPClient(f.Server.Client()))
	require.NoError(t, err)
	fake := &fakeReadiness{report: report}
	router := httpapi.NewRouter(httpapi.Dependencies{Logger: logger},
		NewReadinessHandler(fake, func() time.Time { return now }, auth.NewMiddleware(verifier), logger))
	return router, f, fake
}

func TestReadinessShape(t *testing.T) {
	router, f, _ := newReadinessHarness(t, servicehealth.Report{
		Services: []servicehealth.Status{
			{Name: "order-service", State: servicehealth.Up, Status: "degraded", LatencyMillis: 12,
				Dependencies: map[string]string{"postgres": "ok", "rabbitmq": "degraded"}},
			{Name: "supplier-service", State: servicehealth.Up, Status: "ready", LatencyMillis: 8},
			{Name: "credit-service", State: servicehealth.Down, Reason: servicehealth.ReasonTimeout},
		},
		NotChecked: []servicehealth.NotChecked{{Name: "user-service", Reason: "no /readyz endpoint"}},
	})
	token := f.Token(t, f.Claims(auth.AdminAudience, []string{string(auth.DashboardRead)}), "one")
	request := httptest.NewRequest(http.MethodGet, "/v1/readiness", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
	require.JSONEq(t, `{
		"asOf":"2026-03-02T01:00:00Z",
		"services":[
			{"name":"order-service","state":"up","status":"degraded","dependencies":{"postgres":"ok","rabbitmq":"degraded"},"latencyMillis":12},
			{"name":"supplier-service","state":"up","status":"ready","latencyMillis":8},
			{"name":"credit-service","state":"down","reason":"timeout","latencyMillis":0}],
		"notChecked":[{"name":"user-service","reason":"no /readyz endpoint"}]}`, recorder.Body.String())
}

func TestReadinessEmptyListsAreListsNotNull(t *testing.T) {
	router, f, _ := newReadinessHarness(t, servicehealth.Report{})
	token := f.Token(t, f.Claims(auth.AdminAudience, []string{string(auth.DashboardRead)}), "one")
	request := httptest.NewRequest(http.MethodGet, "/v1/readiness", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	require.JSONEq(t, `{"asOf":"2026-03-02T01:00:00Z","services":[],"notChecked":[]}`, recorder.Body.String())
}

func TestReadinessRequiresDashboardRead(t *testing.T) {
	router, f, fake := newReadinessHarness(t, servicehealth.Report{})
	for name, tc := range map[string]struct {
		token  string
		status int
		code   apperror.Code
	}{
		"no token":                {"", http.StatusUnauthorized, apperror.Unauthenticated},
		"user without permission": {f.Token(t, f.Claims(auth.AdminAudience, []string{"errands:use"}), "one"), http.StatusForbidden, apperror.Forbidden},
		"service token":           {f.Token(t, f.ServiceClaims(auth.AdminAudience, []string{string(auth.DashboardRead)}), "one"), http.StatusForbidden, apperror.Forbidden},
	} {
		request := httptest.NewRequest(http.MethodGet, "/v1/readiness", nil)
		if tc.token != "" {
			request.Header.Set("Authorization", "Bearer "+tc.token)
		}
		recorder := httptest.NewRecorder()

		router.ServeHTTP(recorder, request)

		require.Equal(t, tc.status, recorder.Code, name)
	}
	require.Zero(t, fake.calls, "an unauthorised request never triggers the checks")
}
