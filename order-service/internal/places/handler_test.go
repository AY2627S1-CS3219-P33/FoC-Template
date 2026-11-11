// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-09
// Scope: Implementation of test code (unit and integration tests).
// Author review: Reviewed, validated, and tested by the authors.

package places_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/auth/authtest"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/httpapi"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/places"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/testfakes"
)

const (
	userID  = "auth0|requester-1"
	otherID = "auth0|other-1"

	supplierID        = "0b8e6a52-3c1d-4f7e-8a9b-1c2d3e4f5a6b"
	otherSupplierID   = "2c3d4e5f-6a7b-4c8d-9e0f-1a2b3c4d5e6f"
	versionID         = "5d2c1b0a-9f8e-4d7c-8b6a-5f4e3d2c1b0a"
	otherVersionID    = "6e5d4c3b-2a1f-4e0d-9c8b-7a6f5e4d3c2b"
	buildingID        = "7a6b5c4d-3e2f-4a1b-9c8d-7e6f5a4b3c2d"
	retiredBuildingID = "8b7c6d5e-4f3a-4b2c-8d9e-0f1a2b3c4d5e"
	missingID         = "9c8d7e6f-5a4b-4c3d-8e2f-1a0b9c8d7e6f"
	deliveryDetail    = "Level 2 lounge"

	placesPath = "/me/places"
)

var t0 = time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)

type harness struct {
	router    http.Handler
	store     *testfakes.FakePlaces
	suppliers *testfakes.FakeSupplierGate
	clock     *testfakes.FakeClock
	logs      *strings.Builder
	fixture   *authtest.Fixture
	user      string
	other     string
}

func newHarness(t *testing.T) harness {
	t.Helper()
	return newHarnessWith(t, nil)
}

// newHarnessWith serves the handler with gate in front of the fake supplier
// gate, or the fake itself when gate is nil.
func newHarnessWith(t *testing.T, gate func(*testfakes.FakeSupplierGate) errand.SupplierGate) harness {
	t.Helper()
	f := authtest.New(t)
	cfg := auth.DefaultAuth0Config()
	cfg.Issuer = f.Server.URL + "/"
	verifier, err := auth.NewAuth0(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), auth.WithHTTPClient(f.Server.Client()))
	require.NoError(t, err)

	logs := &strings.Builder{}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	clock := testfakes.NewFakeClock(t0)
	store := testfakes.NewFakePlaces(clock)
	suppliers := testfakes.NewFakeSupplierGate()
	suppliers.AddSupplier(errand.SupplierVersion{
		SupplierID: supplierID, VersionID: versionID, Name: "Kopi Corner",
		Building: "COM1", Floor: "1", LocationDescription: "next to the lift",
		OpeningTime: "08:00", ClosingTime: "20:00",
	})
	suppliers.AddSupplier(errand.SupplierVersion{
		SupplierID: otherSupplierID, VersionID: otherVersionID, Name: "Night Owl",
		Building: "UTown", Floor: "B1", OpeningTime: "22:00", ClosingTime: "02:00",
	})
	suppliers.SetBuildings(
		errand.Building{ID: strings.ToUpper(buildingID), Name: "COM1"},
		errand.Building{ID: retiredBuildingID, Name: "Old Block", Retired: true},
	)
	var ports errand.SupplierGate = suppliers
	if gate != nil {
		ports = gate(suppliers)
	}
	handler := places.NewHandler(places.Dependencies{
		Store: store, Suppliers: ports, Clock: clock, Logger: logger,
	}, auth.NewMiddleware(verifier))
	return harness{
		router: httpapi.NewRouter(httpapi.Dependencies{Logger: logger}, handler),
		store:  store, suppliers: suppliers, clock: clock, logs: logs, fixture: f,
		user:  tokenFor(t, f, userID, auth.UseErrands),
		other: tokenFor(t, f, otherID, auth.UseErrands),
	}
}

func tokenFor(t *testing.T, f *authtest.Fixture, subject string, permissions ...auth.Permission) string {
	t.Helper()
	granted := make([]string, 0, len(permissions))
	for _, p := range permissions {
		granted = append(granted, string(p))
	}
	claims := f.Claims(auth.OrderAudience, granted)
	claims["sub"] = subject
	return f.Token(t, claims, "one")
}

func (h harness) do(t *testing.T, token, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, target, reader)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	h.router.ServeHTTP(recorder, request)
	return recorder
}

// put stores a place for user directly, one minute after the previous one.
func (h harness) put(t *testing.T, p places.NewPlace) places.Place {
	t.Helper()
	h.clock.Advance(time.Minute)
	stored, err := h.store.Create(context.Background(), p)
	require.NoError(t, err)
	return stored
}

func deliveryPlace(user, label, building string) places.NewPlace {
	return places.NewPlace{
		UserID: user, Kind: places.KindDelivery, Label: label,
		DeliveryBuildingID: building, DeliveryDetail: deliveryDetail,
	}
}

func pickupPlace(user, label, supplier string) places.NewPlace {
	return places.NewPlace{UserID: user, Kind: places.KindPickup, Label: label, SupplierID: supplier}
}

type listBody struct {
	Items []map[string]any `json:"items"`
}

func decodeList(t *testing.T, recorder *httptest.ResponseRecorder) listBody {
	t.Helper()
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var body listBody
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	require.NotNil(t, body.Items, "items is [] when empty, never null")
	return body
}

func decodeItem(t *testing.T, recorder *httptest.ResponseRecorder, status int) map[string]any {
	t.Helper()
	require.Equal(t, status, recorder.Code, recorder.Body.String())
	var body map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	return body
}

func labels(body listBody) []string {
	out := make([]string, 0, len(body.Items))
	for _, item := range body.Items {
		out = append(out, item["label"].(string))
	}
	return out
}

func requireError(t *testing.T, recorder *httptest.ResponseRecorder, status int, code apperror.Code) apperror.Error {
	t.Helper()
	require.Equal(t, status, recorder.Code, recorder.Body.String())
	var body apperror.Error
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	require.Equal(t, code, body.Code, recorder.Body.String())
	return body
}

// requireFields checks a VALIDATION_FAILED body names exactly fields.
func requireFields(t *testing.T, recorder *httptest.ResponseRecorder, fields ...string) {
	t.Helper()
	body := requireError(t, recorder, http.StatusBadRequest, apperror.ValidationFailed)
	got := make([]string, 0, len(body.Fields))
	for _, f := range body.Fields {
		got = append(got, f.Field)
	}
	assert.ElementsMatch(t, fields, got, recorder.Body.String())
}

func TestList(t *testing.T) {
	h := newHarness(t)
	home := h.put(t, deliveryPlace(userID, "Home", buildingID))
	h.put(t, pickupPlace(userID, "Coffee", supplierID))
	h.put(t, deliveryPlace(otherID, "Not mine", buildingID))

	body := decodeList(t, h.do(t, h.user, http.MethodGet, placesPath, ""))
	require.Equal(t, []string{"Coffee", "Home"}, labels(body), "newest first, only the caller's")

	assert.Equal(t, map[string]any{
		"placeId":    body.Items[0]["placeId"],
		"kind":       "PICKUP",
		"label":      "Coffee",
		"supplierId": supplierID,
		"supplier": map[string]any{
			"name": "Kopi Corner", "building": "COM1", "floor": "1",
			"openingTime": "08:00", "closingTime": "20:00",
		},
		"available": true,
		"createdAt": t0.Add(2 * time.Minute).Format(time.RFC3339),
	}, body.Items[0])
	assert.Equal(t, map[string]any{
		"placeId":          home.ID,
		"kind":             "DELIVERY",
		"label":            "Home",
		"deliveryBuilding": map[string]any{"id": buildingID, "name": "COM1", "retired": false},
		"deliveryDetail":   deliveryDetail,
		"available":        true,
		"createdAt":        t0.Add(time.Minute).Format(time.RFC3339),
	}, body.Items[1])
	assert.Len(t, h.suppliers.CallsTo("ListBuildings"), 1)
	assert.Len(t, h.suppliers.CallsTo("GetCurrent"), 1)
}

func TestListKind(t *testing.T) {
	h := newHarness(t)
	h.put(t, deliveryPlace(userID, "Home", buildingID))
	h.put(t, pickupPlace(userID, "Coffee", supplierID))

	delivery := decodeList(t, h.do(t, h.user, http.MethodGet, placesPath+"?kind=DELIVERY", ""))
	assert.Equal(t, []string{"Home"}, labels(delivery))
	assert.Empty(t, h.suppliers.CallsTo("GetCurrent"), "no PICKUP place, no supplier read")

	pickup := decodeList(t, h.do(t, h.user, http.MethodGet, placesPath+"?kind=PICKUP", ""))
	assert.Equal(t, []string{"Coffee"}, labels(pickup))
	assert.Len(t, h.suppliers.CallsTo("ListBuildings"), 1, "no DELIVERY place, no building read")

	for _, kind := range []string{"", "delivery", "OTHER"} {
		requireError(t, h.do(t, h.user, http.MethodGet, placesPath+"?kind="+kind, ""),
			http.StatusBadRequest, apperror.InvalidArgument)
	}
}

func TestListEmpty(t *testing.T) {
	h := newHarness(t)
	assert.Empty(t, decodeList(t, h.do(t, h.user, http.MethodGet, placesPath, "")).Items)
	assert.Empty(t, h.suppliers.Calls(), "nothing to resolve")
}

func TestListStalePlaces(t *testing.T) {
	h := newHarness(t)
	h.put(t, deliveryPlace(userID, "Old office", retiredBuildingID))
	h.put(t, deliveryPlace(userID, "Gone", missingID))
	h.put(t, pickupPlace(userID, "Closed now", otherSupplierID))
	h.put(t, pickupPlace(userID, "Deleted", supplierID))
	h.suppliers.SetUnavailable(supplierID)
	h.suppliers.SetClosed(otherSupplierID)

	body := decodeList(t, h.do(t, h.user, http.MethodGet, placesPath, ""))
	require.Equal(t, []string{"Deleted", "Closed now", "Gone", "Old office"}, labels(body))

	deleted := body.Items[0]
	assert.Equal(t, false, deleted["available"])
	assert.Equal(t, supplierID, deleted["supplierId"])
	assert.NotContains(t, deleted, "supplier")

	closed := body.Items[1]
	assert.Equal(t, true, closed["available"], "opening hours do not matter")
	assert.Equal(t, "Night Owl", closed["supplier"].(map[string]any)["name"])

	gone := body.Items[2]
	assert.Equal(t, false, gone["available"])
	assert.Equal(t, map[string]any{"id": missingID, "name": "", "retired": false}, gone["deliveryBuilding"])

	retired := body.Items[3]
	assert.Equal(t, false, retired["available"])
	assert.Equal(t, map[string]any{"id": retiredBuildingID, "name": "Old Block", "retired": true}, retired["deliveryBuilding"])

	// Stale places are marked, never removed.
	stored, err := h.store.List(context.Background(), userID, "")
	require.NoError(t, err)
	assert.Len(t, stored, 4)
}

// countingGate counts concurrent GetCurrent calls.
type countingGate struct {
	*testfakes.FakeSupplierGate
	inFlight, peak atomic.Int32
}

func (g *countingGate) GetCurrent(ctx context.Context, supplierID string) (errand.SupplierVersion, error) {
	n := g.inFlight.Add(1)
	defer g.inFlight.Add(-1)
	for {
		peak := g.peak.Load()
		if n <= peak || g.peak.CompareAndSwap(peak, n) {
			break
		}
	}
	time.Sleep(10 * time.Millisecond)
	return g.FakeSupplierGate.GetCurrent(ctx, supplierID)
}

func TestListReadsSuppliersConcurrentlyUpToFive(t *testing.T) {
	var gate *countingGate
	h := newHarnessWith(t, func(fake *testfakes.FakeSupplierGate) errand.SupplierGate {
		gate = &countingGate{FakeSupplierGate: fake}
		return gate
	})
	for i := range places.MaxPerKind {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		h.suppliers.AddSupplier(errand.SupplierVersion{SupplierID: id, VersionID: id, Name: fmt.Sprintf("Shop %d", i)})
		h.put(t, pickupPlace(userID, fmt.Sprintf("Shop %d", i), id))
	}

	body := decodeList(t, h.do(t, h.user, http.MethodGet, placesPath, ""))
	require.Len(t, body.Items, places.MaxPerKind)
	for _, item := range body.Items {
		assert.Equal(t, item["label"], item["supplier"].(map[string]any)["name"], "each item has its own supplier")
	}
	assert.Len(t, h.suppliers.CallsTo("GetCurrent"), places.MaxPerKind)
	assert.Greater(t, gate.peak.Load(), int32(1))
	assert.LessOrEqual(t, gate.peak.Load(), int32(5))
}

func TestListFailures(t *testing.T) {
	down := &apperror.Error{Code: apperror.DependencyUnavailable, Message: "down"}

	t.Run("supplier-service down for one supplier is 503", func(t *testing.T) {
		h := newHarness(t)
		h.put(t, pickupPlace(userID, "Coffee", supplierID))
		h.put(t, pickupPlace(userID, "Late", otherSupplierID))
		h.suppliers.FailNext("GetCurrent", down)
		requireError(t, h.do(t, h.user, http.MethodGet, placesPath, ""), http.StatusServiceUnavailable, apperror.DependencyUnavailable)
	})

	t.Run("supplier-service down for buildings is 503", func(t *testing.T) {
		h := newHarness(t)
		h.put(t, deliveryPlace(userID, "Home", buildingID))
		h.suppliers.FailNext("ListBuildings", down)
		requireError(t, h.do(t, h.user, http.MethodGet, placesPath, ""), http.StatusServiceUnavailable, apperror.DependencyUnavailable)
	})

	t.Run("a store failure is 500 and logs no request data", func(t *testing.T) {
		h := newHarness(t)
		h.store.FailNext("List", errors.New("connection reset"))
		requireError(t, h.do(t, h.user, http.MethodGet, placesPath, ""), http.StatusInternalServerError, apperror.Internal)
		assert.Contains(t, h.logs.String(), "connection reset")
		assert.NotContains(t, h.logs.String(), userID)
	})
}

func TestCreateDelivery(t *testing.T) {
	h := newHarness(t)
	recorder := h.do(t, h.user, http.MethodPost, placesPath,
		`{"kind":"DELIVERY","label":"Home","deliveryBuildingId":"`+strings.ToUpper(buildingID)+`","deliveryDetail":"`+deliveryDetail+`"}`)
	item := decodeItem(t, recorder, http.StatusCreated)
	assert.Equal(t, "DELIVERY", item["kind"])
	assert.Equal(t, "Home", item["label"])
	assert.Equal(t, map[string]any{"id": buildingID, "name": "COM1", "retired": false}, item["deliveryBuilding"])
	assert.Equal(t, deliveryDetail, item["deliveryDetail"])
	assert.Equal(t, true, item["available"])
	assert.Equal(t, t0.Format(time.RFC3339), item["createdAt"])
	assert.NotContains(t, item, "supplierId")

	stored, err := h.store.List(context.Background(), userID, places.KindDelivery)
	require.NoError(t, err)
	require.Len(t, stored, 1)
	assert.Equal(t, item["placeId"], stored[0].ID)
	assert.Equal(t, buildingID, stored[0].DeliveryBuildingID, "stored in lower case")
	assert.Empty(t, h.suppliers.CallsTo("GetCurrent"))
}

func TestCreatePickup(t *testing.T) {
	h := newHarness(t)
	// A closed supplier can still be saved.
	h.suppliers.SetClosed(otherSupplierID)
	item := decodeItem(t, h.do(t, h.user, http.MethodPost, placesPath,
		`{"kind":"PICKUP","label":"Supper","supplierId":"`+otherSupplierID+`"}`), http.StatusCreated)
	assert.Equal(t, "PICKUP", item["kind"])
	assert.Equal(t, otherSupplierID, item["supplierId"])
	assert.Equal(t, map[string]any{
		"name": "Night Owl", "building": "UTown", "floor": "B1",
		"openingTime": "22:00", "closingTime": "02:00",
	}, item["supplier"])
	assert.Equal(t, true, item["available"])
	assert.NotContains(t, item, "deliveryBuilding")
	assert.NotContains(t, item, "deliveryDetail")
	assert.Empty(t, h.suppliers.CallsTo("ResolveForCreate"), "opening hours are not checked")
	assert.Empty(t, h.suppliers.CallsTo("ListBuildings"))
}

func TestCreateValidation(t *testing.T) {
	long := strings.Repeat("é", places.MaxLabelLength+1)
	for name, tc := range map[string]struct {
		body   string
		fields []string
	}{
		"empty object":            {`{}`, []string{"kind", "label"}},
		"unknown kind":            {`{"kind":"HOME","label":"Home"}`, []string{"kind"}},
		"kind in lower case":      {`{"kind":"pickup","label":"Home","supplierId":"` + supplierID + `"}`, []string{"kind"}},
		"kind not a string":       {`{"kind":1,"label":"Home"}`, []string{"kind"}},
		"unknown kind and member": {`{"kind":"HOME","label":"Home","colour":"red"}`, []string{"kind", "colour"}},
		"blank label":             {`{"kind":"PICKUP","label":"  ","supplierId":"` + supplierID + `"}`, []string{"label"}},
		"long label":              {`{"kind":"PICKUP","label":"` + long + `","supplierId":"` + supplierID + `"}`, []string{"label"}},
		"pickup without supplier": {`{"kind":"PICKUP","label":"Coffee"}`, []string{"supplierId"}},
		"pickup with delivery fields": {
			`{"kind":"PICKUP","label":"Coffee","supplierId":"` + supplierID + `","deliveryBuildingId":"` + buildingID + `","deliveryDetail":"x"}`,
			[]string{"deliveryBuildingId", "deliveryDetail"},
		},
		"malformed supplier": {`{"kind":"PICKUP","label":"Coffee","supplierId":"nope"}`, []string{"supplierId"}},
		"delivery without building or detail": {
			`{"kind":"DELIVERY","label":"Home"}`, []string{"deliveryBuildingId", "deliveryDetail"},
		},
		"delivery with supplier": {
			`{"kind":"DELIVERY","label":"Home","deliveryBuildingId":"` + buildingID + `","deliveryDetail":"x","supplierId":"` + supplierID + `"}`,
			[]string{"supplierId"},
		},
		"blank detail": {
			`{"kind":"DELIVERY","label":"Home","deliveryBuildingId":"` + buildingID + `","deliveryDetail":" "}`,
			[]string{"deliveryDetail"},
		},
		"long detail": {
			`{"kind":"DELIVERY","label":"Home","deliveryBuildingId":"` + buildingID + `","deliveryDetail":"` + strings.Repeat("x", 201) + `"}`,
			[]string{"deliveryDetail"},
		},
		"null label": {`{"kind":"PICKUP","label":null,"supplierId":"` + supplierID + `"}`, []string{"label"}},
		"contact details are not saved": {
			`{"kind":"DELIVERY","label":"Home","deliveryBuildingId":"` + buildingID + `","deliveryDetail":"x","contactName":"Alex","contactMobile":"+65 9123 4567"}`,
			[]string{"contactMobile", "contactName"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			requireFields(t, h.do(t, h.user, http.MethodPost, placesPath, tc.body), tc.fields...)
			assert.Empty(t, h.suppliers.Calls(), "supplier-service is not asked about an invalid body")
		})
	}

	t.Run("a label of 40 characters is accepted", func(t *testing.T) {
		h := newHarness(t)
		label := strings.Repeat("é", places.MaxLabelLength)
		decodeItem(t, h.do(t, h.user, http.MethodPost, placesPath,
			`{"kind":"PICKUP","label":"`+label+`","supplierId":"`+supplierID+`"}`), http.StatusCreated)
	})

	for name, body := range map[string]string{
		"no body":      "",
		"not JSON":     "{",
		"not object":   "[]",
		"two objects":  "{}{}",
		"too large":    `{"label":"` + strings.Repeat("x", 5<<10) + `"}`,
		"null literal": "null",
	} {
		t.Run(name+" is INVALID_ARGUMENT", func(t *testing.T) {
			h := newHarness(t)
			requireError(t, h.do(t, h.user, http.MethodPost, placesPath, body), http.StatusBadRequest, apperror.InvalidArgument)
		})
	}
}

func TestCreateChecksSupplierService(t *testing.T) {
	down := &apperror.Error{Code: apperror.DependencyUnavailable, Message: "down"}

	for name, building := range map[string]string{"retired": retiredBuildingID, "unknown": missingID} {
		t.Run("an "+name+" building is invalid", func(t *testing.T) {
			h := newHarness(t)
			requireFields(t, h.do(t, h.user, http.MethodPost, placesPath,
				`{"kind":"DELIVERY","label":"Home","deliveryBuildingId":"`+building+`","deliveryDetail":"x"}`), "deliveryBuildingId")
			assert.Empty(t, mustList(t, h, userID))
		})
	}

	for name, supplier := range map[string]string{"deleted": supplierID, "unknown": missingID} {
		t.Run("a "+name+" supplier is invalid", func(t *testing.T) {
			h := newHarness(t)
			h.suppliers.SetUnavailable(supplierID)
			requireFields(t, h.do(t, h.user, http.MethodPost, placesPath,
				`{"kind":"PICKUP","label":"Coffee","supplierId":"`+supplier+`"}`), "supplierId")
			assert.Empty(t, mustList(t, h, userID))
		})
	}

	t.Run("supplier-service down is 503", func(t *testing.T) {
		h := newHarness(t)
		h.suppliers.FailNext("GetCurrent", down)
		requireError(t, h.do(t, h.user, http.MethodPost, placesPath,
			`{"kind":"PICKUP","label":"Coffee","supplierId":"`+supplierID+`"}`), http.StatusServiceUnavailable, apperror.DependencyUnavailable)
		h.suppliers.FailNext("ListBuildings", down)
		requireError(t, h.do(t, h.user, http.MethodPost, placesPath,
			`{"kind":"DELIVERY","label":"Home","deliveryBuildingId":"`+buildingID+`","deliveryDetail":"x"}`), http.StatusServiceUnavailable, apperror.DependencyUnavailable)
		assert.Empty(t, mustList(t, h, userID))
	})
}

func TestCreateStoreRules(t *testing.T) {
	t.Run("a duplicate label ignoring case is invalid", func(t *testing.T) {
		h := newHarness(t)
		h.put(t, pickupPlace(userID, "Coffee", supplierID))
		requireFields(t, h.do(t, h.user, http.MethodPost, placesPath,
			`{"kind":"PICKUP","label":"COFFEE","supplierId":"`+otherSupplierID+`"}`), "label")
		// The same label of the other kind, or another user's, is fine.
		decodeItem(t, h.do(t, h.user, http.MethodPost, placesPath,
			`{"kind":"DELIVERY","label":"Coffee","deliveryBuildingId":"`+buildingID+`","deliveryDetail":"x"}`), http.StatusCreated)
		decodeItem(t, h.do(t, h.other, http.MethodPost, placesPath,
			`{"kind":"PICKUP","label":"Coffee","supplierId":"`+otherSupplierID+`"}`), http.StatusCreated)
	})

	t.Run("a supplier saved twice is invalid", func(t *testing.T) {
		h := newHarness(t)
		h.put(t, pickupPlace(userID, "Coffee", supplierID))
		requireFields(t, h.do(t, h.user, http.MethodPost, placesPath,
			`{"kind":"PICKUP","label":"Again","supplierId":"`+supplierID+`"}`), "supplierId")
	})

	t.Run("the limit per kind is 409", func(t *testing.T) {
		h := newHarness(t)
		for i := range places.MaxPerKind {
			h.put(t, deliveryPlace(userID, fmt.Sprintf("Place %d", i), buildingID))
		}
		requireError(t, h.do(t, h.user, http.MethodPost, placesPath,
			`{"kind":"DELIVERY","label":"One more","deliveryBuildingId":"`+buildingID+`","deliveryDetail":"x"}`),
			http.StatusConflict, apperror.PlaceLimitReached)
		decodeItem(t, h.do(t, h.user, http.MethodPost, placesPath,
			`{"kind":"PICKUP","label":"Coffee","supplierId":"`+supplierID+`"}`), http.StatusCreated)
	})

	t.Run("a store failure is 500 and logs no request data", func(t *testing.T) {
		h := newHarness(t)
		h.store.FailNext("Create", errors.New("connection reset"))
		requireError(t, h.do(t, h.user, http.MethodPost, placesPath,
			`{"kind":"DELIVERY","label":"Secret label","deliveryBuildingId":"`+buildingID+`","deliveryDetail":"Room 42"}`),
			http.StatusInternalServerError, apperror.Internal)
		assert.Contains(t, h.logs.String(), "connection reset")
		for _, data := range []string{userID, "Secret label", "Room 42"} {
			assert.NotContains(t, h.logs.String(), data)
		}
	})
}

func TestUpdate(t *testing.T) {
	t.Run("label and detail of a delivery place", func(t *testing.T) {
		h := newHarness(t)
		home := h.put(t, deliveryPlace(userID, "Home", buildingID))
		item := decodeItem(t, h.do(t, h.user, http.MethodPatch, placesPath+"/"+strings.ToUpper(home.ID),
			`{"label":"Hall","deliveryDetail":"Block B, level 7"}`), http.StatusOK)
		assert.Equal(t, home.ID, item["placeId"])
		assert.Equal(t, "Hall", item["label"])
		assert.Equal(t, "Block B, level 7", item["deliveryDetail"])
		assert.Equal(t, map[string]any{"id": buildingID, "name": "COM1", "retired": false}, item["deliveryBuilding"])
		assert.Equal(t, true, item["available"])
	})

	t.Run("label of a pickup place", func(t *testing.T) {
		h := newHarness(t)
		coffee := h.put(t, pickupPlace(userID, "Coffee", supplierID))
		item := decodeItem(t, h.do(t, h.user, http.MethodPatch, placesPath+"/"+coffee.ID, `{"label":"Kopi"}`), http.StatusOK)
		assert.Equal(t, "Kopi", item["label"])
		assert.Equal(t, "Kopi Corner", item["supplier"].(map[string]any)["name"])
	})

	t.Run("a stale place can still be renamed", func(t *testing.T) {
		h := newHarness(t)
		coffee := h.put(t, pickupPlace(userID, "Coffee", supplierID))
		h.suppliers.SetUnavailable(supplierID)
		item := decodeItem(t, h.do(t, h.user, http.MethodPatch, placesPath+"/"+coffee.ID, `{"label":"Gone"}`), http.StatusOK)
		assert.Equal(t, false, item["available"])
	})

	t.Run("delivery detail of a pickup place is invalid", func(t *testing.T) {
		h := newHarness(t)
		coffee := h.put(t, pickupPlace(userID, "Coffee", supplierID))
		requireFields(t, h.do(t, h.user, http.MethodPatch, placesPath+"/"+coffee.ID, `{"deliveryDetail":"x"}`), "deliveryDetail")
	})

	t.Run("a duplicate label is invalid", func(t *testing.T) {
		h := newHarness(t)
		h.put(t, deliveryPlace(userID, "Home", buildingID))
		hall := h.put(t, deliveryPlace(userID, "Hall", buildingID))
		requireFields(t, h.do(t, h.user, http.MethodPatch, placesPath+"/"+hall.ID, `{"label":"home"}`), "label")
	})

	t.Run("invalid fields", func(t *testing.T) {
		h := newHarness(t)
		home := h.put(t, deliveryPlace(userID, "Home", buildingID))
		requireFields(t, h.do(t, h.user, http.MethodPatch, placesPath+"/"+home.ID,
			`{"label":" ","deliveryDetail":"`+strings.Repeat("x", 201)+`","kind":"PICKUP","supplierId":"`+supplierID+`"}`),
			"label", "deliveryDetail", "kind", "supplierId")
	})

	for name, body := range map[string]string{"empty object": `{}`, "only nulls": `{"label":null}`, "not JSON": `{`} {
		t.Run(name+" is INVALID_ARGUMENT", func(t *testing.T) {
			h := newHarness(t)
			home := h.put(t, deliveryPlace(userID, "Home", buildingID))
			requireError(t, h.do(t, h.user, http.MethodPatch, placesPath+"/"+home.ID, body), http.StatusBadRequest, apperror.InvalidArgument)
		})
	}

	t.Run("another user's, a missing or a malformed place is 404", func(t *testing.T) {
		h := newHarness(t)
		home := h.put(t, deliveryPlace(userID, "Home", buildingID))
		for _, id := range []string{home.ID, missingID} {
			requireError(t, h.do(t, h.other, http.MethodPatch, placesPath+"/"+id, `{"label":"Mine"}`), http.StatusNotFound, apperror.PlaceNotFound)
		}
		// A malformed ID is 404 even with an invalid body.
		requireError(t, h.do(t, h.user, http.MethodPatch, placesPath+"/not-a-uuid", `{}`), http.StatusNotFound, apperror.PlaceNotFound)
		assert.Equal(t, "Home", mustList(t, h, userID)[0].Label)
	})

	t.Run("supplier-service down after the update is 503", func(t *testing.T) {
		h := newHarness(t)
		home := h.put(t, deliveryPlace(userID, "Home", buildingID))
		h.suppliers.FailNext("ListBuildings", &apperror.Error{Code: apperror.DependencyUnavailable, Message: "down"})
		requireError(t, h.do(t, h.user, http.MethodPatch, placesPath+"/"+home.ID, `{"label":"Hall"}`),
			http.StatusServiceUnavailable, apperror.DependencyUnavailable)
		assert.Equal(t, "Hall", mustList(t, h, userID)[0].Label, "the update stands; a retry repeats it")
	})

	t.Run("a store failure is 500", func(t *testing.T) {
		h := newHarness(t)
		home := h.put(t, deliveryPlace(userID, "Home", buildingID))
		h.store.FailNext("Update", errors.New("connection reset"))
		requireError(t, h.do(t, h.user, http.MethodPatch, placesPath+"/"+home.ID, `{"label":"Hall"}`), http.StatusInternalServerError, apperror.Internal)
	})
}

func TestDelete(t *testing.T) {
	h := newHarness(t)
	home := h.put(t, deliveryPlace(userID, "Home", buildingID))

	requireError(t, h.do(t, h.other, http.MethodDelete, placesPath+"/"+home.ID, ""), http.StatusNotFound, apperror.PlaceNotFound)
	requireError(t, h.do(t, h.user, http.MethodDelete, placesPath+"/not-a-uuid", ""), http.StatusNotFound, apperror.PlaceNotFound)
	require.Len(t, mustList(t, h, userID), 1)

	recorder := h.do(t, h.user, http.MethodDelete, placesPath+"/"+strings.ToUpper(home.ID), "")
	require.Equal(t, http.StatusNoContent, recorder.Code, recorder.Body.String())
	assert.Empty(t, recorder.Body.String())
	assert.Empty(t, mustList(t, h, userID))
	assert.Empty(t, h.suppliers.Calls(), "deleting needs no supplier-service")

	requireError(t, h.do(t, h.user, http.MethodDelete, placesPath+"/"+home.ID, ""), http.StatusNotFound, apperror.PlaceNotFound)

	h.store.FailNext("Delete", errors.New("connection reset"))
	requireError(t, h.do(t, h.user, http.MethodDelete, placesPath+"/"+missingID, ""), http.StatusInternalServerError, apperror.Internal)
}

func TestAuthorization(t *testing.T) {
	h := newHarness(t)
	home := h.put(t, deliveryPlace(userID, "Home", buildingID))
	admin := tokenFor(t, h.fixture, userID, auth.ReviewErrands)
	service := h.fixture.Token(t, h.fixture.ServiceClaims(auth.OrderAudience, []string{string(auth.UseErrands)}), "one")
	for _, route := range []struct{ method, path, body string }{
		{http.MethodGet, placesPath, ""},
		{http.MethodPost, placesPath, `{"kind":"PICKUP","label":"Coffee","supplierId":"` + supplierID + `"}`},
		{http.MethodPatch, placesPath + "/" + home.ID, `{"label":"Hall"}`},
		{http.MethodDelete, placesPath + "/" + home.ID, ""},
	} {
		requireError(t, h.do(t, "", route.method, route.path, route.body), http.StatusUnauthorized, apperror.Unauthenticated)
		requireError(t, h.do(t, admin, route.method, route.path, route.body), http.StatusForbidden, apperror.Forbidden)
		requireError(t, h.do(t, service, route.method, route.path, route.body), http.StatusForbidden, apperror.Forbidden)
	}
	assert.Equal(t, []string{"Home"}, labelsOf(mustList(t, h, userID)), "nothing changed")
}

func TestHandlerIsSafeForConcurrentRequests(t *testing.T) {
	h := newHarness(t)
	var wg sync.WaitGroup
	for i := range 2 * places.MaxPerKind {
		wg.Go(func() {
			h.do(t, h.user, http.MethodPost, placesPath,
				fmt.Sprintf(`{"kind":"DELIVERY","label":"Place %d","deliveryBuildingId":"%s","deliveryDetail":"x"}`, i, buildingID))
		})
	}
	wg.Wait()
	assert.Len(t, mustList(t, h, userID), places.MaxPerKind)
}

func mustList(t *testing.T, h harness, user string) []places.Place {
	t.Helper()
	stored, err := h.store.List(context.Background(), user, "")
	require.NoError(t, err)
	return stored
}

func labelsOf(ps []places.Place) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Label)
	}
	return out
}
