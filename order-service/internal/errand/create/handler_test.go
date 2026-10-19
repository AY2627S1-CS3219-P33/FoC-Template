package create_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/auth/authtest"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand/create"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/httpapi"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/testfakes"
)

const (
	requesterID       = "auth0|fixture-user" // authtest.Fixture.Claims' subject
	supplierID        = "0b8e6a52-3c1d-4f7e-8a9b-1c2d3e4f5a6b"
	versionID         = "5d2c1b0a-9f8e-4d7c-8b6a-5f4e3d2c1b0a"
	buildingID        = "7a6b5c4d-3e2f-4a1b-9c8d-7e6f5a4b3c2d"
	retiredBuildingID = "8b7c6d5e-4f3a-4b2c-8d9e-8f7a6b5c4d3e"
	contactName       = "Alex Tan"
	contactMobile     = "+65 9123 4567"

	key  = "9f8e7d6c-5b4a-4392-8170-6f5e4d3c2b1a"
	key2 = "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d"
)

var t0 = time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)

type harness struct {
	router    http.Handler
	store     *testfakes.FakeStore
	suppliers *testfakes.FakeSupplierGate
	clock     *testfakes.FakeClock
	logs      *strings.Builder
	fixture   *authtest.Fixture
	token     string // requesterID with errands:use
}

func newHarness(t *testing.T) harness {
	t.Helper()
	f := authtest.New(t)
	cfg := auth.DefaultAuth0Config()
	cfg.Issuer = f.Server.URL + "/"
	verifier, err := auth.NewAuth0(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), auth.WithHTTPClient(f.Server.Client()))
	require.NoError(t, err)

	logs := &strings.Builder{}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	store := testfakes.NewFakeStore()
	suppliers := testfakes.NewFakeSupplierGate()
	suppliers.AddSupplier(errand.SupplierVersion{
		SupplierID: supplierID, VersionID: versionID, Name: "Kopi Corner",
		Building: "COM1", Floor: "1", LocationDescription: "next to the lift",
		Latitude: 1.2949, Longitude: 103.7737, OpeningTime: "08:00", ClosingTime: "20:00",
	})
	suppliers.SetBuildings(
		errand.Building{ID: buildingID, Name: "COM1"},
		errand.Building{ID: retiredBuildingID, Name: "Old Block", Retired: true},
	)
	clock := testfakes.NewFakeClock(t0)
	handler := create.NewHandler(create.Dependencies{
		Store: store, Suppliers: suppliers, Clock: clock, Logger: logger,
	}, auth.NewMiddleware(verifier))
	router := httpapi.NewRouter(httpapi.Dependencies{Logger: logger}, handler)
	return harness{
		router: router, store: store, suppliers: suppliers, clock: clock, logs: logs, fixture: f,
		token: tokenFor(t, f, requesterID, auth.UseErrands),
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

func (h harness) post(t *testing.T, token, idempotencyKey, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/errands", strings.NewReader(body))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	recorder := httptest.NewRecorder()
	h.router.ServeHTTP(recorder, request)
	return recorder
}

// body is a valid ErrandCreate with overrides applied; a nil override
// removes the member.
func body(t *testing.T, overrides map[string]any) string {
	t.Helper()
	b := map[string]any{
		"supplierId":         supplierID,
		"deliveryBuildingId": buildingID,
		"deliveryDetail":     "Level 2 lounge",
		"items":              []any{map[string]any{"description": "iced latte", "quantity": 2}},
		"notes":              "less ice",
		"contactName":        contactName,
		"contactMobile":      contactMobile,
		"reward":             10,
	}
	for k, v := range overrides {
		if v == nil {
			delete(b, k)
			continue
		}
		b[k] = v
	}
	raw, err := json.Marshal(b)
	require.NoError(t, err)
	return string(raw)
}

func decode(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &out), recorder.Body.String())
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

func TestCreate(t *testing.T) {
	h := newHarness(t)
	recorder := h.post(t, h.token, key, body(t, nil))
	require.Equal(t, http.StatusAccepted, recorder.Code, recorder.Body.String())

	got := decode(t, recorder)
	id, _ := got["errandId"].(string)
	require.Regexp(t, `^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`, id)
	assert.Equal(t, "/errands/"+id, recorder.Header().Get("Location"))
	assert.Equal(t, "application/json", recorder.Header().Get("Content-Type"))
	assert.JSONEq(t, fmt.Sprintf(`{
		"errandId": %q,
		"version": 1,
		"status": "PENDING_RESERVATION",
		"supplierId": %q,
		"supplierVersionId": %q,
		"pickup": {"name": "Kopi Corner", "building": "COM1", "floor": "1",
			"locationDescription": "next to the lift", "latitude": 1.2949, "longitude": 103.7737},
		"deliveryBuilding": {"id": %q, "name": "COM1"},
		"deliveryDetail": "Level 2 lounge",
		"items": [{"description": "iced latte", "quantity": 2}],
		"notes": "less ice",
		"reward": 10,
		"createdAt": "2026-03-02T09:00:00Z",
		"updatedAt": "2026-03-02T09:00:00Z",
		"acceptanceDeadline": "2026-03-02T10:00:00Z",
		"viewerRole": "REQUESTER",
		"contactName": %q,
		"contactMobile": %q
	}`, id, supplierID, versionID, buildingID, contactName, contactMobile), recorder.Body.String())

	stored, ok := h.store.Errand(id)
	require.True(t, ok)
	assert.Equal(t, errand.StatusPendingReservation, stored.Status)
	assert.Equal(t, requesterID, stored.RequesterID)
	assert.Equal(t, key, stored.CreateIdempotencyKey)
	assert.JSONEq(t, `{"id":"`+buildingID+`","name":"COM1","retired":false}`, string(stored.Details.DeliveryBuildingSnapshot))

	outbox := h.store.Outbox()
	require.Len(t, outbox, 2)
	assert.Equal(t, errand.KindCreditReserve, outbox[0].Kind)
	assert.Equal(t, id+":reserve", outbox[0].IdempotencyKey)
	assert.Equal(t, errand.ErrandPlaced.Kind(), outbox[1].Kind)

	calls := h.suppliers.CallsTo("ResolveForCreate")
	require.Len(t, calls, 1)
	assert.Equal(t, []any{supplierID, t0}, calls[0].Args)
}

func TestCreateNormalisesInput(t *testing.T) {
	h := newHarness(t)
	deadline := t0.Add(30 * time.Minute).In(time.FixedZone("SGT", 8*60*60))
	recorder := h.post(t, h.token, strings.ToUpper(key), body(t, map[string]any{
		"supplierId":         strings.ToUpper(supplierID),
		"deliveryBuildingId": strings.ToUpper(buildingID),
		"notes":              nil,
		"acceptanceDeadline": deadline.Format(time.RFC3339),
	}))
	require.Equal(t, http.StatusAccepted, recorder.Code, recorder.Body.String())

	got := decode(t, recorder)
	assert.Equal(t, supplierID, got["supplierId"])
	assert.Equal(t, "2026-03-02T09:30:00Z", got["acceptanceDeadline"])
	assert.Equal(t, "", got["notes"])
	assert.Equal(t, map[string]any{"id": buildingID, "name": "COM1"}, got["deliveryBuilding"])

	stored, ok := h.store.Errand(got["errandId"].(string))
	require.True(t, ok)
	assert.Equal(t, key, stored.CreateIdempotencyKey, "the key is stored in lower case")
}

func TestCreateReplay(t *testing.T) {
	t.Run("returns the original errand whatever the body", func(t *testing.T) {
		h := newHarness(t)
		first := h.post(t, h.token, key, body(t, nil))
		require.Equal(t, http.StatusAccepted, first.Code, first.Body.String())

		// A closed supplier and a body that is not even JSON change nothing.
		h.suppliers.SetUnavailable(supplierID)
		h.clock.Advance(time.Minute)
		replay := h.post(t, h.token, key, "not json")
		require.Equal(t, http.StatusOK, replay.Code, replay.Body.String())
		assert.JSONEq(t, first.Body.String(), replay.Body.String())
		assert.Empty(t, replay.Header().Get("Location"))

		assert.Len(t, h.store.Outbox(), 2, "nothing new was written")
		assert.Len(t, h.suppliers.CallsTo("ResolveForCreate"), 1)
	})

	t.Run("returns the errand in its current state", func(t *testing.T) {
		h := newHarness(t)
		first := decode(t, h.post(t, h.token, key, body(t, nil)))
		id := first["errandId"].(string)
		_, _, err := h.store.Fire(t.Context(), id, errand.EventReservationConfirmed, errand.SystemActor(), errand.EventInput{}, t0.Add(time.Second))
		require.NoError(t, err)

		replay := h.post(t, h.token, key, body(t, nil))
		require.Equal(t, http.StatusOK, replay.Code, replay.Body.String())
		got := decode(t, replay)
		assert.Equal(t, "OPEN", got["status"])
		assert.EqualValues(t, 2, got["version"])
	})

	t.Run("a concurrent duplicate that loses the race gets the winner's errand", func(t *testing.T) {
		h := newHarness(t)
		first := h.post(t, h.token, key, body(t, nil))
		require.Equal(t, http.StatusAccepted, first.Code)

		// The second request's replay lookup ran before the first committed.
		h.store.FailNext("Get", &apperror.Error{Code: apperror.ErrandNotFound, Message: "not found"})
		second := h.post(t, h.token, key, body(t, map[string]any{"reward": 99}))
		require.Equal(t, http.StatusOK, second.Code, second.Body.String())
		assert.JSONEq(t, first.Body.String(), second.Body.String())
		assert.Len(t, h.store.Outbox(), 2)
	})

	t.Run("keys are per requester and per submission", func(t *testing.T) {
		h := newHarness(t)
		mine := decode(t, h.post(t, h.token, key, body(t, nil)))
		other := h.post(t, tokenFor(t, h.fixture, "auth0|requester-2", auth.UseErrands), key, body(t, nil))
		require.Equal(t, http.StatusAccepted, other.Code, other.Body.String())
		again := h.post(t, h.token, key2, body(t, nil))
		require.Equal(t, http.StatusAccepted, again.Code, again.Body.String())

		ids := map[any]bool{mine["errandId"]: true, decode(t, other)["errandId"]: true, decode(t, again)["errandId"]: true}
		assert.Len(t, ids, 3)
	})
}

func TestCreateValidation(t *testing.T) {
	long := func(n int) string { return strings.Repeat("a", n) }
	at := func(d time.Duration) string { return t0.Add(d).Format(time.RFC3339) }
	manyItems := make([]any, 21)
	for i := range manyItems {
		manyItems[i] = map[string]any{"description": "bun", "quantity": 1}
	}

	cases := []struct {
		name   string
		body   string
		fields []apperror.Field
	}{
		{"every required field missing", `{}`, []apperror.Field{
			{Field: "supplierId", Message: "is required"},
			{Field: "deliveryBuildingId", Message: "is required"},
			{Field: "deliveryDetail", Message: "is required"},
			{Field: "items", Message: "is required"},
			{Field: "contactName", Message: "is required"},
			{Field: "contactMobile", Message: "is required"},
			{Field: "reward", Message: "is required"},
		}},
		{"null is missing", body(t, map[string]any{"reward": json.RawMessage("null")}), []apperror.Field{
			{Field: "reward", Message: "is required"},
		}},
		{"wrong types", body(t, map[string]any{
			"supplierId": 5, "deliveryBuildingId": "not-a-uuid", "items": "latte", "reward": "10",
		}), []apperror.Field{
			{Field: "supplierId", Message: "must be a string"},
			{Field: "deliveryBuildingId", Message: "must be a UUID"},
			{Field: "items", Message: "must be an array"},
			{Field: "reward", Message: "must be a positive integer"},
		}},
		{"blank and too long text", body(t, map[string]any{
			"deliveryDetail": "   ", "notes": long(501), "contactName": long(101), "contactMobile": "+65\u0007",
		}), []apperror.Field{
			{Field: "deliveryDetail", Message: "must not be blank"},
			{Field: "notes", Message: "must be at most 500 characters"},
			{Field: "contactName", Message: "must be at most 100 characters"},
			{Field: "contactMobile", Message: "must not contain control characters"},
		}},
		{"no items", body(t, map[string]any{"items": []any{}}), []apperror.Field{
			{Field: "items", Message: "must list at least one item"},
		}},
		{"too many items", body(t, map[string]any{"items": manyItems}), []apperror.Field{
			{Field: "items", Message: "must list at most 20 items"},
		}},
		{"invalid items", body(t, map[string]any{"items": []any{
			map[string]any{"description": " ", "quantity": 0, "size": "L"},
			"latte",
			map[string]any{"description": long(201), "quantity": 1.5},
		}}), []apperror.Field{
			{Field: "items[0].description", Message: "must not be blank"},
			{Field: "items[0].quantity", Message: "must be an integer from 1 to 99"},
			{Field: "items[0].size", Message: "is not allowed"},
			{Field: "items[1]", Message: "must be an object"},
			{Field: "items[2].description", Message: "must be at most 200 characters"},
			{Field: "items[2].quantity", Message: "must be an integer from 1 to 99"},
		}},
		{"zero reward", body(t, map[string]any{"reward": 0}), []apperror.Field{
			{Field: "reward", Message: "must be a positive integer"},
		}},
		{"fractional reward", body(t, map[string]any{"reward": 1.5}), []apperror.Field{
			{Field: "reward", Message: "must be a positive integer"},
		}},
		{"reward beyond int32", body(t, map[string]any{"reward": 2147483648}), []apperror.Field{
			{Field: "reward", Message: "must be a positive integer"},
		}},
		{"unparseable deadline", body(t, map[string]any{"acceptanceDeadline": "tomorrow"}), []apperror.Field{
			{Field: "acceptanceDeadline", Message: "must be an RFC 3339 date-time"},
		}},
		{"past deadline", body(t, map[string]any{"acceptanceDeadline": at(-time.Minute)}), []apperror.Field{
			{Field: "acceptanceDeadline", Message: "must be in the future"},
		}},
		{"deadline too soon", body(t, map[string]any{"acceptanceDeadline": at(9 * time.Minute)}), []apperror.Field{
			{Field: "acceptanceDeadline", Message: "must be at least 10 minutes from now"},
		}},
		{"deadline too late", body(t, map[string]any{"acceptanceDeadline": at(24*time.Hour + time.Second)}), []apperror.Field{
			{Field: "acceptanceDeadline", Message: "must be at most 24 hours from now"},
		}},
		{"identity in the body", body(t, map[string]any{"requesterId": "auth0|someone-else", "status": "OPEN"}), []apperror.Field{
			{Field: "requesterId", Message: "is not allowed"},
			{Field: "status", Message: "is not allowed"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			recorder := h.post(t, h.token, key, tc.body)
			got := requireError(t, recorder, http.StatusBadRequest, apperror.ValidationFailed)
			assert.Equal(t, tc.fields, got.Fields)
			assert.Empty(t, h.store.Outbox())
			assert.Empty(t, h.suppliers.Calls(), "nothing is resolved for an invalid body")
		})
	}

	t.Run("deadline bounds are inclusive", func(t *testing.T) {
		for _, d := range []time.Duration{10 * time.Minute, 24 * time.Hour} {
			h := newHarness(t)
			recorder := h.post(t, h.token, key, body(t, map[string]any{"acceptanceDeadline": at(d)}))
			assert.Equal(t, http.StatusAccepted, recorder.Code, recorder.Body.String())
		}
	})
}

func TestCreateMalformedRequest(t *testing.T) {
	cases := []struct {
		name, key, body string
	}{
		{"no idempotency key", "", body(t, nil)},
		{"idempotency key not a UUID", "retry-1", body(t, nil)},
		{"empty body", key, ""},
		{"array body", key, `[]`},
		{"null body", key, `null`},
		{"two objects", key, `{} {}`},
		{"too large", key, `{"notes":"` + strings.Repeat("a", 70<<10) + `"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			requireError(t, h.post(t, h.token, tc.key, tc.body), http.StatusBadRequest, apperror.InvalidArgument)
			assert.Empty(t, h.store.Outbox())
		})
	}
}

func TestCreateDeliveryBuilding(t *testing.T) {
	cases := map[string]struct {
		building, message string
	}{
		"unknown": {"3c4d5e6f-7a8b-4c9d-8e0f-1a2b3c4d5e6f", "must be an existing building"},
		"retired": {retiredBuildingID, "must not be a retired building"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			recorder := h.post(t, h.token, key, body(t, map[string]any{"deliveryBuildingId": tc.building}))
			got := requireError(t, recorder, http.StatusBadRequest, apperror.ValidationFailed)
			assert.Equal(t, []apperror.Field{{Field: "deliveryBuildingId", Message: tc.message}}, got.Fields)
			assert.Empty(t, h.store.Outbox())
		})
	}
}

func TestCreateSupplierAndDependencyErrors(t *testing.T) {
	dependencyDown := &apperror.Error{Code: apperror.DependencyUnavailable, Message: "supplier-service is unavailable"}
	cases := []struct {
		name   string
		setup  func(h harness)
		status int
		code   apperror.Code
	}{
		{"supplier deleted or closed", func(h harness) { h.suppliers.SetUnavailable(supplierID) },
			http.StatusUnprocessableEntity, apperror.SupplierUnavailable},
		{"supplier-service down on resolve", func(h harness) { h.suppliers.FailNext("ResolveForCreate", dependencyDown) },
			http.StatusServiceUnavailable, apperror.DependencyUnavailable},
		{"supplier-service down on buildings", func(h harness) { h.suppliers.FailNext("ListBuildings", dependencyDown) },
			http.StatusServiceUnavailable, apperror.DependencyUnavailable},
		{"supplier being deleted", func(h harness) { h.store.Fence(supplierID) },
			http.StatusConflict, apperror.SupplierDeletionInProgress},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			tc.setup(h)
			requireError(t, h.post(t, h.token, key, body(t, nil)), tc.status, tc.code)
			assert.Empty(t, h.store.Outbox(), "nothing is created")

			// The same key works once the problem is gone.
			h.suppliers.AddSupplier(errand.SupplierVersion{SupplierID: supplierID, VersionID: versionID, Name: "Kopi Corner"})
			h.store.Unfence(supplierID)
			recorder := h.post(t, h.token, key, body(t, nil))
			assert.Equal(t, http.StatusAccepted, recorder.Code, recorder.Body.String())
		})
	}
}

func TestCreateInternalErrorIsHiddenAndLogged(t *testing.T) {
	h := newHarness(t)
	h.store.FailNext("Create", errors.New("connection reset by peer"))
	got := requireError(t, h.post(t, h.token, key, body(t, nil)), http.StatusInternalServerError, apperror.Internal)
	assert.NotContains(t, got.Message, "connection reset")

	logs := h.logs.String()
	assert.Contains(t, logs, "connection reset by peer")
	assert.Contains(t, logs, "errand_id=")
	assert.NotContains(t, logs, contactName, "the contact person is never logged")
	assert.NotContains(t, logs, contactMobile)
}

func TestCreateAuthorization(t *testing.T) {
	h := newHarness(t)
	requireError(t, h.post(t, "", key, body(t, nil)), http.StatusUnauthorized, apperror.Unauthenticated)
	requireError(t, h.post(t, tokenFor(t, h.fixture, requesterID, auth.ReviewErrands), key, body(t, nil)),
		http.StatusForbidden, apperror.Forbidden)
	service := h.fixture.Token(t, h.fixture.ServiceClaims(auth.OrderAudience, []string{string(auth.UseErrands)}), "one")
	requireError(t, h.post(t, service, key, body(t, nil)), http.StatusForbidden, apperror.Forbidden)
	assert.Empty(t, h.store.Outbox())
}
