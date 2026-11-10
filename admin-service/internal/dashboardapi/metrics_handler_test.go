package dashboardapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/metrics"
)

var (
	sgt = func() *time.Location { l, _ := time.LoadLocation("Asia/Singapore"); return l }()
	// 2026-03-02 09:00 in Singapore.
	now = time.Date(2026, 3, 2, 9, 0, 0, 0, sgt)
)

// fakeMetrics returns canned results and records the periods and limits.
type fakeMetrics struct {
	periods []metrics.Period
	limits  []int
	err     error
	called  int
}

func (f *fakeMetrics) Location() *time.Location { return sgt }

func (f *fakeMetrics) note(p metrics.Period) error {
	f.called++
	f.periods = append(f.periods, p)
	return f.err
}

func (f *fakeMetrics) ErrandsPerDay(_ context.Context, p metrics.Period) ([]metrics.DayCount, error) {
	var out []metrics.DayCount
	for i, day := range p.Days() {
		out = append(out, metrics.DayCount{Date: day, Created: i})
	}
	return out, f.note(p)
}

func (f *fakeMetrics) TimeToAccept(_ context.Context, p metrics.Period) (metrics.TimeToAccept, error) {
	median, p90 := 95.5, 456.0
	return metrics.TimeToAccept{Accepted: 12, MedianSeconds: &median, P90Seconds: &p90}, f.note(p)
}

func (f *fakeMetrics) Outcomes(_ context.Context, p metrics.Period) (metrics.Outcomes, error) {
	return metrics.Outcomes{Finished: 10, Items: []metrics.Outcome{
		{Status: "COMPLETED", Count: 7, Rate: 0.7}, {Status: "CANCELLED", Count: 1, Rate: 0.1},
		{Status: "EXPIRED", Count: 2, Rate: 0.2}, {Status: "REJECTED"}, {Status: "REFUNDED"},
	}}, f.note(p)
}

func (f *fakeMetrics) TopSuppliers(_ context.Context, p metrics.Period, limit int) (metrics.Ranking, error) {
	f.limits = append(f.limits, limit)
	name := "Kopi Corner"
	return metrics.Ranking{Items: []metrics.Ranked{
		{ID: "s1", Name: &name, Finished: 4, Completed: 3}, {ID: "s2", Finished: 1, Completed: 1},
	}, Unattributed: 2}, f.note(p)
}

func (f *fakeMetrics) TopBuildings(_ context.Context, p metrics.Period, limit int) (metrics.Ranking, error) {
	f.limits = append(f.limits, limit)
	return metrics.Ranking{}, f.note(p)
}

func (f *fakeMetrics) ActiveCouriers(_ context.Context, p metrics.Period) (int, error) {
	return 3, f.note(p)
}

func (f *fakeMetrics) RewardVolume(_ context.Context, p metrics.Period) ([]metrics.DayReward, error) {
	var out []metrics.DayReward
	for _, day := range p.Days() {
		out = append(out, metrics.DayReward{Date: day, Reward: 10, Completed: 2})
	}
	return out, f.note(p)
}

type harness struct {
	router  http.Handler
	fixture *authtest.Fixture
	metrics *fakeMetrics
	admin   string // a user token with dashboard:read
	logs    *bytes.Buffer
}

func newHarness(t *testing.T) harness {
	t.Helper()
	f := authtest.New(t)
	cfg := auth.DefaultAuth0Config()
	cfg.Issuer = f.Server.URL + "/"
	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	verifier, err := auth.NewAuth0(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), auth.WithHTTPClient(f.Server.Client()))
	require.NoError(t, err)
	fake := &fakeMetrics{}
	router := httpapi.NewRouter(httpapi.Dependencies{Logger: logger},
		NewMetricsHandler(fake, func() time.Time { return now }, auth.NewMiddleware(verifier), logger))
	admin := f.Token(t, f.Claims(auth.AdminAudience, []string{string(auth.DashboardRead)}), "one")
	return harness{router: router, fixture: f, metrics: fake, admin: admin, logs: logs}
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

func decodeError(t *testing.T, recorder *httptest.ResponseRecorder) apperror.Error {
	t.Helper()
	var body apperror.Error
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	return body
}

const asOfHeader = `"asOf":"2026-03-02T01:00:00Z","timezone":"Asia/Singapore"`

func TestErrandsPerDayShapeAndDefaultPeriod(t *testing.T) {
	h := newHarness(t)

	recorder := h.get(t, "/v1/metrics/errands-per-day?from=2026-03-01&to=2026-03-02", h.admin)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
	require.JSONEq(t, `{`+asOfHeader+`,"from":"2026-03-01","to":"2026-03-02",
		"days":[{"date":"2026-03-01","created":0},{"date":"2026-03-02","created":1}]}`, recorder.Body.String())

	// No parameters: the last 30 days ending today in Singapore.
	h = newHarness(t)
	recorder = h.get(t, "/v1/metrics/errands-per-day", h.admin)
	require.Equal(t, http.StatusOK, recorder.Code)
	var body struct {
		From, To string
		Days     []any
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	require.Equal(t, "2026-02-01", body.From)
	require.Equal(t, "2026-03-02", body.To)
	require.Len(t, body.Days, 30)
}

func TestTimeToAcceptShape(t *testing.T) {
	h := newHarness(t)

	recorder := h.get(t, "/v1/metrics/time-to-accept?from=2026-03-02&to=2026-03-02", h.admin)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.JSONEq(t, `{`+asOfHeader+`,"from":"2026-03-02","to":"2026-03-02","accepted":12,"medianSeconds":95.5,"p90Seconds":456}`, recorder.Body.String())
}

func TestOutcomesShapeHasAllFiveStatuses(t *testing.T) {
	h := newHarness(t)

	recorder := h.get(t, "/v1/metrics/outcomes?from=2026-03-02&to=2026-03-02", h.admin)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.JSONEq(t, `{`+asOfHeader+`,"from":"2026-03-02","to":"2026-03-02","finished":10,"outcomes":[
		{"status":"COMPLETED","count":7,"rate":0.7},{"status":"CANCELLED","count":1,"rate":0.1},
		{"status":"EXPIRED","count":2,"rate":0.2},{"status":"REJECTED","count":0,"rate":0},
		{"status":"REFUNDED","count":0,"rate":0}]}`, recorder.Body.String())
}

func TestRankingsShapeAndLimit(t *testing.T) {
	h := newHarness(t)

	recorder := h.get(t, "/v1/metrics/top-suppliers?from=2026-03-02&to=2026-03-02&limit=2", h.admin)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.JSONEq(t, `{`+asOfHeader+`,"from":"2026-03-02","to":"2026-03-02","basis":"finished","unattributed":2,
		"items":[{"id":"s1","name":"Kopi Corner","finished":4,"completed":3},{"id":"s2","name":null,"finished":1,"completed":1}]}`,
		recorder.Body.String())
	require.Equal(t, []int{2}, h.metrics.limits)

	recorder = h.get(t, "/v1/metrics/top-buildings", h.admin)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"items":[]`, "an empty ranking is a list, not null")
	require.Equal(t, []int{2, metrics.DefaultRankLimit}, h.metrics.limits, "the default length")
}

func TestActiveCouriersAndRewardVolumeShape(t *testing.T) {
	h := newHarness(t)

	couriers := h.get(t, "/v1/metrics/active-couriers?from=2026-03-02&to=2026-03-02", h.admin)
	require.Equal(t, http.StatusOK, couriers.Code, couriers.Body.String())
	require.JSONEq(t, `{`+asOfHeader+`,"from":"2026-03-02","to":"2026-03-02","couriers":3}`, couriers.Body.String())

	rewards := h.get(t, "/v1/metrics/reward-volume?from=2026-03-01&to=2026-03-02", h.admin)
	require.Equal(t, http.StatusOK, rewards.Code, rewards.Body.String())
	require.JSONEq(t, `{`+asOfHeader+`,"from":"2026-03-01","to":"2026-03-02","total":20,"completed":4,
		"days":[{"date":"2026-03-01","reward":10,"completed":2},{"date":"2026-03-02","reward":10,"completed":2}]}`, rewards.Body.String())
}

func TestEveryMetricRouteRejectsABadPeriodAndABadLimit(t *testing.T) {
	h := newHarness(t)
	routes := []string{"errands-per-day", "time-to-accept", "outcomes", "top-suppliers", "top-buildings", "active-couriers", "reward-volume"}

	for _, route := range routes {
		for _, query := range []string{"from=yesterday", "to=2026-13-01", "from=2026-03-02&to=2026-03-01", "from=2020-01-01&to=2026-03-01"} {
			recorder := h.get(t, "/v1/metrics/"+route+"?"+query, h.admin)

			require.Equal(t, http.StatusBadRequest, recorder.Code, "%s?%s: %s", route, query, recorder.Body.String())
			require.Equal(t, apperror.InvalidArgument, decodeError(t, recorder).Code)
		}
	}
	for _, route := range []string{"top-suppliers", "top-buildings"} {
		for _, limit := range []string{"0", "51", "-1", "x", "1.5"} {
			recorder := h.get(t, "/v1/metrics/"+route+"?limit="+limit, h.admin)

			require.Equal(t, http.StatusBadRequest, recorder.Code, "%s limit=%s", route, limit)
		}
	}
	require.Zero(t, h.metrics.called, "a refused request never reaches the metrics")
}

func TestMetricRoutesRequireDashboardRead(t *testing.T) {
	h := newHarness(t)
	f := h.fixture
	userWithout := f.Token(t, f.Claims(auth.AdminAudience, []string{"errands:use"}), "one")
	service := f.Token(t, f.ServiceClaims(auth.AdminAudience, []string{string(auth.DashboardRead)}), "one")
	otherAudience := f.Token(t, f.Claims("https://api.foc.local/order-service", []string{string(auth.DashboardRead)}), "one")
	tests := []struct {
		name, token string
		status      int
		code        apperror.Code
	}{
		{"no token", "", http.StatusUnauthorized, apperror.Unauthenticated},
		{"garbage token", "not.a.jwt", http.StatusUnauthorized, apperror.Unauthenticated},
		{"user without dashboard:read", userWithout, http.StatusForbidden, apperror.Forbidden},
		{"service token with the permission", service, http.StatusForbidden, apperror.Forbidden},
		{"token for another API", otherAudience, http.StatusUnauthorized, apperror.Unauthenticated},
	}
	for _, route := range []string{"errands-per-day", "time-to-accept", "outcomes", "top-suppliers", "top-buildings", "active-couriers", "reward-volume"} {
		for _, tt := range tests {
			recorder := h.get(t, "/v1/metrics/"+route, tt.token)

			require.Equal(t, tt.status, recorder.Code, "%s: %s", route, tt.name)
			require.Equal(t, tt.code, decodeError(t, recorder).Code, "%s: %s", route, tt.name)
		}
	}
	require.Zero(t, h.metrics.called)
}

func TestMetricFailureIsHiddenAndLogged(t *testing.T) {
	h := newHarness(t)
	h.metrics.err = errors.New("connection to postgres://admin:secret@db:5432 reset")

	for _, route := range []string{"errands-per-day", "time-to-accept", "outcomes", "top-suppliers", "top-buildings", "active-couriers", "reward-volume"} {
		recorder := h.get(t, "/v1/metrics/"+route, h.admin)

		require.Equal(t, http.StatusInternalServerError, recorder.Code, route)
		require.Equal(t, apperror.Internal, decodeError(t, recorder).Code)
		require.NotContains(t, recorder.Body.String(), "secret")
	}
	require.Contains(t, h.logs.String(), "metrics request failed")
}

func TestOnlyGetIsAllowed(t *testing.T) {
	h := newHarness(t)
	request := httptest.NewRequest(http.MethodPost, "/v1/metrics/outcomes", nil)
	request.Header.Set("Authorization", "Bearer "+h.admin)
	recorder := httptest.NewRecorder()

	h.router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusMethodNotAllowed, recorder.Code)
}
