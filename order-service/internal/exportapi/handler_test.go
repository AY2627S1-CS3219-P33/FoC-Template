// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-09
// Scope: Implementation of test code (unit and integration tests).
// Author review: Reviewed, validated, and tested by the authors.

package exportapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/auth/authtest"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/opsrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/httpapi"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/serviceauth"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/testfakes"
)

const (
	errandA = "6f1c2b0e-8d4a-4e1b-9c3f-2a7d5e8b9c10"
	errandB = "7a2d3c1f-9e5b-4f2c-8d4a-3b8e6f9c0d21"
	errandC = "8b3e4d2a-0f6c-4a3d-9e5b-4c9f7a0d1e32"
	kopiV1  = "11111111-1111-4111-8111-111111111111"
	kopiV2  = "11111111-1111-4111-8111-222222222222"
	deleted = "33333333-3333-4333-8333-333333333333"
)

var t0 = time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)

// fakeSource serves canned rows and records the arguments.
type fakeSource struct {
	transitions []opsrepo.ExportedTransition
	errands     []opsrepo.ExportedErrand
	err         error

	afterIDs []int64
	limits   []int
	byID     [][]string
}

func (f *fakeSource) ExportTransitions(_ context.Context, afterID int64, limit int) ([]opsrepo.ExportedTransition, error) {
	f.afterIDs = append(f.afterIDs, afterID)
	f.limits = append(f.limits, limit)
	if f.err != nil {
		return nil, f.err
	}
	var out []opsrepo.ExportedTransition
	for _, t := range f.transitions {
		if t.ID > afterID && len(out) < limit {
			out = append(out, t)
		}
	}
	return out, nil
}

func (f *fakeSource) ExportErrands(_ context.Context, afterID string, limit int) ([]opsrepo.ExportedErrand, error) {
	f.limits = append(f.limits, limit)
	if f.err != nil {
		return nil, f.err
	}
	var out []opsrepo.ExportedErrand
	for _, e := range f.errands {
		if e.ID > afterID && len(out) < limit {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeSource) ExportErrandsByID(_ context.Context, ids []string) ([]opsrepo.ExportedErrand, error) {
	f.byID = append(f.byID, ids)
	if f.err != nil {
		return nil, f.err
	}
	var out []opsrepo.ExportedErrand
	for _, e := range f.errands {
		for _, id := range ids {
			if e.ID == id {
				out = append(out, e)
			}
		}
	}
	return out, nil
}

type harness struct {
	router    http.Handler
	fixture   *authtest.Fixture
	source    *fakeSource
	suppliers *testfakes.FakeSupplierGate
	service   string // a service token with orders:admin-read
	logs      *bytes.Buffer
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
	guard := serviceauth.NewMiddleware(auth.NewMiddleware(verifier))

	suppliers := testfakes.NewFakeSupplierGate()
	suppliers.AddSupplier(errand.SupplierVersion{SupplierID: "s1", VersionID: kopiV1, Name: "Kopi Corner"})
	suppliers.AddSupplier(errand.SupplierVersion{SupplierID: "s1", VersionID: kopiV2, Name: "Kopi Corner Reborn"})
	suppliers.AddSupplier(errand.SupplierVersion{SupplierID: "s2", VersionID: deleted, Name: "Old Stall"})
	suppliers.SetUnavailable("s2")
	source := &fakeSource{}
	router := httpapi.NewRouter(httpapi.Dependencies{Logger: logger}, NewHandler(source, suppliers, guard, logger))
	token := f.Token(t, f.ServiceClaims(auth.OrderAudience, []string{string(auth.AdminRead)}), "one")
	return harness{router: router, fixture: f, source: source, suppliers: suppliers, service: token, logs: logs}
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

func seedTransitions(h harness, n int) {
	for i := 1; i <= n; i++ {
		h.source.transitions = append(h.source.transitions, opsrepo.ExportedTransition{
			ID: int64(i * 10), MessageID: errandA + ":accepted:" + string(rune('0'+i)), Type: "errand.accepted",
			ErrandID: errandA, ErrandVersion: i, Event: errand.EventAccept,
			From: errand.StatusOpen, To: errand.StatusAssigned, CourierID: "auth0|cou1", Reward: 8,
			OccurredAt: t0.Add(time.Duration(i) * time.Minute),
		})
	}
}

func TestTransitionsShape(t *testing.T) {
	h := newHarness(t)
	h.source.transitions = []opsrepo.ExportedTransition{
		{ID: 4, MessageID: errandA + ":placed:1", Type: "errand.placed", ErrandID: errandA, ErrandVersion: 1,
			Event: errand.EventSubmit, To: errand.StatusPendingReservation, Reward: 8, OccurredAt: t0},
		{ID: 9, MessageID: errandA + ":accepted:3", Type: "errand.accepted", ErrandID: errandA, ErrandVersion: 3,
			Event: errand.EventAccept, From: errand.StatusOpen, To: errand.StatusAssigned,
			CourierID: "auth0|cou1", Reward: 8, OccurredAt: t0.Add(time.Minute)},
	}

	recorder := h.get(t, "/internal/export/transitions", h.service)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, "application/json", recorder.Header().Get("Content-Type"))
	require.JSONEq(t, `{"items":[
		{"id":4,"messageId":"`+errandA+`:placed:1","type":"errand.placed","errandId":"`+errandA+`","errandVersion":1,
		 "event":"submit","to":"PENDING_RESERVATION","reward":8,"occurredAt":"2026-03-02T09:00:00Z"},
		{"id":9,"messageId":"`+errandA+`:accepted:3","type":"errand.accepted","errandId":"`+errandA+`","errandVersion":3,
		 "event":"accept","from":"OPEN","to":"ASSIGNED","courierId":"auth0|cou1","reward":8,"occurredAt":"2026-03-02T09:01:00Z"}
	]}`, recorder.Body.String(), "from and courierId are omitted when empty; no actor, requester or reason")
	require.Equal(t, []int{DefaultPageSize + 1}, h.source.limits, "one extra row tells whether there is a next page")
}

func TestTransitionsPageWithKeyset(t *testing.T) {
	h := newHarness(t)
	seedTransitions(h, 5)

	var seen []int64
	path := "/internal/export/transitions?limit=2"
	for page := 0; ; page++ {
		require.Less(t, page, 5)
		recorder := h.get(t, path, h.service)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		var body transitionPage
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
		for _, item := range body.Items {
			seen = append(seen, item.ID)
		}
		if body.NextCursor == "" {
			break
		}
		path = "/internal/export/transitions?limit=2&after=" + body.NextCursor
	}

	require.Equal(t, []int64{10, 20, 30, 40, 50}, seen)
	require.Equal(t, []int64{0, 20, 40}, h.source.afterIDs)
}

func TestTransitionsExactlyFullLastPageHasNoCursor(t *testing.T) {
	h := newHarness(t)
	seedTransitions(h, 2)

	recorder := h.get(t, "/internal/export/transitions?limit=2", h.service)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.NotContains(t, recorder.Body.String(), "nextCursor")
}

func TestExportRejectsBadParameters(t *testing.T) {
	h := newHarness(t)
	for _, path := range []string{
		"/internal/export/transitions?limit=0",
		"/internal/export/transitions?limit=501",
		"/internal/export/transitions?limit=x",
		"/internal/export/transitions?after=-1",
		"/internal/export/transitions?after=abc",
		"/internal/export/errands?limit=0",
		"/internal/export/errands?after=not-a-uuid",
		"/internal/export/errands?ids=",
		"/internal/export/errands?ids=" + errandA + ",nope",
		"/internal/export/errands?ids=" + errandA + "&limit=5",
		"/internal/export/errands?ids=" + errandA + "&after=" + errandB,
		"/internal/export/errands?ids=" + strings.Repeat(errandA+",", MaxIDs) + errandA,
	} {
		recorder := h.get(t, path, h.service)
		require.Equal(t, http.StatusBadRequest, recorder.Code, path)
		require.Equal(t, apperror.InvalidArgument, decodeError(t, recorder).Code, path)
	}
	require.Empty(t, h.source.limits, "a refused request never reaches the store")
	require.Empty(t, h.source.byID)

	full := h.get(t, "/internal/export/transitions?limit=500", h.service)
	require.Equal(t, http.StatusOK, full.Code, "the largest page is allowed")
	require.Equal(t, []int{501}, h.source.limits)
}

func exported(id, _ string, status errand.Status, supplierVersion string) opsrepo.ExportedErrand {
	return opsrepo.ExportedErrand{
		ID: id, Status: status, Version: 3, CourierID: "auth0|cou1", Reward: 8,
		SupplierID: "ssss", SupplierVersionID: supplierVersion,
		DeliveryBuildingID: "bbbb", DeliveryBuildingName: "COM1",
		CreatedAt: t0, UpdatedAt: t0.Add(time.Hour),
	}
}

func TestErrandsShapeAndSupplierNames(t *testing.T) {
	h := newHarness(t)
	open := exported(errandA, "", errand.StatusOpen, kopiV1)
	open.CourierID = ""
	open.DeliveryBuildingName = ""
	h.source.errands = []opsrepo.ExportedErrand{
		open,
		exported(errandB, "", errand.StatusAssigned, kopiV2),
		exported(errandC, "", errand.StatusCompleted, deleted),
	}

	recorder := h.get(t, "/internal/export/errands", h.service)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.JSONEq(t, `{"items":[
		{"errandId":"`+errandA+`","status":"OPEN","version":3,"courierId":null,"reward":8,"supplierId":"ssss",
		 "supplierName":"Kopi Corner","deliveryBuildingId":"bbbb","deliveryBuildingName":null,
		 "createdAt":"2026-03-02T09:00:00Z","updatedAt":"2026-03-02T10:00:00Z"},
		{"errandId":"`+errandB+`","status":"ASSIGNED","version":3,"courierId":"auth0|cou1","reward":8,"supplierId":"ssss",
		 "supplierName":"Kopi Corner Reborn","deliveryBuildingId":"bbbb","deliveryBuildingName":"COM1",
		 "createdAt":"2026-03-02T09:00:00Z","updatedAt":"2026-03-02T10:00:00Z"},
		{"errandId":"`+errandC+`","status":"COMPLETED","version":3,"courierId":"auth0|cou1","reward":8,"supplierId":"ssss",
		 "supplierName":"Old Stall","deliveryBuildingId":"bbbb","deliveryBuildingName":"COM1",
		 "createdAt":"2026-03-02T09:00:00Z","updatedAt":"2026-03-02T10:00:00Z"}
	]}`, recorder.Body.String(), "the name follows the errand's supplier version, also for a deleted supplier")
	for _, forbidden := range []string{"contactName", "contactMobile", "notes", "requesterId", "deliveryDetail", "supplierVersionId"} {
		require.NotContains(t, recorder.Body.String(), forbidden)
	}
}

func TestErrandsWhenSupplierServiceIsDown(t *testing.T) {
	h := newHarness(t)
	h.source.errands = []opsrepo.ExportedErrand{
		exported(errandA, "", errand.StatusOpen, kopiV1),
		exported(errandB, "", errand.StatusOpen, kopiV2),
	}
	h.suppliers.FailAlways("GetVersion", &apperror.Error{Code: apperror.DependencyUnavailable, Message: "supplier-service is unavailable"})

	recorder := h.get(t, "/internal/export/errands", h.service)

	require.Equal(t, http.StatusOK, recorder.Code, "the rows are returned without names, not refused: %s", recorder.Body.String())
	var body struct {
		Items []map[string]any `json:"items"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	require.Len(t, body.Items, 2)
	for _, item := range body.Items {
		require.Nil(t, item["supplierName"])
		require.Equal(t, "COM1", item["deliveryBuildingName"], "the building name needs no supplier-service")
	}
	require.Contains(t, h.logs.String(), "supplier names unavailable")
	require.Len(t, h.suppliers.CallsTo("GetVersion"), 1, "one failure stops further supplier calls")
}

func TestErrandsRecoverOnTheNextRequest(t *testing.T) {
	h := newHarness(t)
	h.source.errands = []opsrepo.ExportedErrand{exported(errandA, "", errand.StatusOpen, kopiV1)}
	h.suppliers.FailNext("GetVersion", errors.New("timeout"))

	first := h.get(t, "/internal/export/errands?ids="+errandA, h.service)
	second := h.get(t, "/internal/export/errands?ids="+errandA, h.service)

	require.Contains(t, first.Body.String(), `"supplierName":null`)
	require.Contains(t, second.Body.String(), `"supplierName":"Kopi Corner"`)
}

func TestErrandsByIDs(t *testing.T) {
	h := newHarness(t)
	h.source.errands = []opsrepo.ExportedErrand{
		exported(errandA, "", errand.StatusOpen, kopiV1),
		exported(errandB, "", errand.StatusOpen, kopiV1),
	}
	missing := "9c4f5e3b-1a7d-4b4e-8f6c-5d0a8b1e2f43"

	recorder := h.get(t, "/internal/export/errands?ids="+strings.ToUpper(errandA)+","+missing, h.service)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var body errandPage
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	require.Len(t, body.Items, 1, "an unknown errand is left out")
	require.Equal(t, errandA, body.Items[0].ErrandID)
	require.Empty(t, body.NextCursor)
	require.Equal(t, [][]string{{errandA, missing}}, h.source.byID, "ids are lower-cased")
}

func TestErrandsPageWithKeyset(t *testing.T) {
	h := newHarness(t)
	h.source.errands = []opsrepo.ExportedErrand{
		exported(errandA, "", errand.StatusOpen, kopiV1),
		exported(errandB, "", errand.StatusOpen, kopiV1),
		exported(errandC, "", errand.StatusOpen, kopiV1),
	}

	var seen []string
	path := "/internal/export/errands?limit=2"
	for page := 0; ; page++ {
		require.Less(t, page, 4)
		recorder := h.get(t, path, h.service)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		var body errandPage
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
		for _, item := range body.Items {
			seen = append(seen, item.ErrandID)
		}
		if body.NextCursor == "" {
			break
		}
		path = "/internal/export/errands?limit=2&after=" + body.NextCursor
	}
	require.Equal(t, []string{errandA, errandB, errandC}, seen)
}

func TestExportRequiresAdminReadServiceToken(t *testing.T) {
	h := newHarness(t)
	f := h.fixture
	adminRead := []string{string(auth.AdminRead)}
	tests := []struct {
		name, token string
		status      int
		code        apperror.Code
	}{
		{"no token", "", http.StatusUnauthorized, apperror.Unauthenticated},
		{"invalid token", "not.a.jwt", http.StatusUnauthorized, apperror.Unauthenticated},
		{"user token with the permission", f.Token(t, f.Claims(auth.OrderAudience, adminRead), "one"), http.StatusForbidden, apperror.Forbidden},
		{"admin user token", f.Token(t, f.Claims(auth.OrderAudience, []string{string(auth.ReviewErrands)}), "one"), http.StatusForbidden, apperror.Forbidden},
		{"service token with orders:internal only", f.Token(t, f.ServiceClaims(auth.OrderAudience, []string{string(auth.InternalAccess)}), "one"), http.StatusForbidden, apperror.Forbidden},
	}
	for _, route := range []string{"/internal/export/transitions", "/internal/export/errands"} {
		for _, tt := range tests {
			recorder := h.get(t, route, tt.token)
			require.Equal(t, tt.status, recorder.Code, "%s: %s", route, tt.name)
			require.Equal(t, tt.code, decodeError(t, recorder).Code, "%s: %s", route, tt.name)
		}
	}
	require.Empty(t, h.source.limits, "a rejected credential never reaches the store")
}

func TestInternalAccessDoesNotGrantExportAndAdminReadDoesNotGrantInternal(t *testing.T) {
	require.False(t, auth.Principal{Subject: "c@clients", Kind: auth.ServiceToken,
		Permissions: []auth.Permission{auth.InternalAccess}}.Has(auth.AdminRead))
	require.False(t, auth.Principal{Subject: "c@clients", Kind: auth.ServiceToken,
		Permissions: []auth.Permission{auth.AdminRead}}.Has(auth.InternalAccess))
}

func TestExportHidesStoreFailure(t *testing.T) {
	h := newHarness(t)
	h.source.err = errors.New("dial tcp postgres://order:secret@db:5432: refused")

	for _, route := range []string{"/internal/export/transitions", "/internal/export/errands"} {
		recorder := h.get(t, route, h.service)
		require.Equal(t, http.StatusInternalServerError, recorder.Code, route)
		require.Equal(t, apperror.Internal, decodeError(t, recorder).Code)
		require.NotContains(t, recorder.Body.String(), "secret")
	}
	require.Contains(t, h.logs.String(), "export request failed")
	require.Contains(t, h.logs.String(), "caller=fixture-client@clients")
}
