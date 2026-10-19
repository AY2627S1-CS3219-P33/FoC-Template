package accept_test

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
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand/accept"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/httpapi"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/testfakes"
)

const (
	requesterID = "auth0|requester-1"
	courierID   = "auth0|courier-1"
	rivalID     = "auth0|courier-2"

	supplierID    = "0b8e6a52-3c1d-4f7e-8a9b-1c2d3e4f5a6b"
	versionID     = "5d2c1b0a-9f8e-4d7c-8b6a-5f4e3d2c1b0a"
	buildingID    = "7a6b5c4d-3e2f-4a1b-9c8d-7e6f5a4b3c2d"
	contactName   = "Alex Tan"
	contactMobile = "+65 9123 4567"

	rejectedKind = "event.errand.accept_rejected"
)

var t0 = time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)

func errandID(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) }

func ptr[T any](v T) *T { return &v }

// openErrand is OPEN at version 2 with an hour left to accept it.
func openErrand(n int) errand.Errand {
	return errand.Errand{
		ID:          errandID(n),
		Status:      errand.StatusOpen,
		Version:     2,
		RequesterID: requesterID,
		Details: errand.Details{
			SupplierID:               supplierID,
			SupplierVersionID:        versionID,
			DeliveryBuildingID:       buildingID,
			DeliveryBuildingSnapshot: json.RawMessage(`{"id":"` + buildingID + `","name":"COM1","retired":false}`),
			DeliveryDetail:           "Level 2 lounge",
			Items:                    json.RawMessage(`[{"description":"iced latte","quantity":2}]`),
			Notes:                    "less ice",
			ContactName:              contactName,
			ContactMobile:            contactMobile,
			Reward:                   10,
			AcceptanceDeadline:       t0.Add(time.Hour),
		},
		CreatedAt: t0.Add(-20 * time.Minute),
		UpdatedAt: t0.Add(-19 * time.Minute),
	}
}

// assignedTo is openErrand(n) ASSIGNED to courier at version 3, with its
// pickup deadline at pickupBy.
func assignedTo(n int, courier string, pickupBy time.Time) errand.Errand {
	e := openErrand(n)
	e.Status, e.Version, e.CourierID = errand.StatusAssigned, 3, courier
	e.PickupDeadline, e.DeliveryDeadline = ptr(pickupBy), ptr(pickupBy.Add(time.Hour))
	return e
}

type harness struct {
	router    http.Handler
	store     *testfakes.FakeStore
	suppliers *testfakes.FakeSupplierGate
	settings  *testfakes.FakeSettings
	clock     *testfakes.FakeClock
	logs      *strings.Builder
	fixture   *authtest.Fixture
	courier   string
	rival     string
	requester string
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
		Latitude: 1.2949, Longitude: 103.7737,
	})
	settings := testfakes.NewFakeSettings()
	clock := testfakes.NewFakeClock(t0)
	handler := accept.NewHandler(accept.Dependencies{
		Store: store, Suppliers: suppliers, Settings: settings, Clock: clock, Logger: logger,
	}, auth.NewMiddleware(verifier))
	return harness{
		router: httpapi.NewRouter(httpapi.Dependencies{Logger: logger}, handler),
		store:  store, suppliers: suppliers, settings: settings, clock: clock, logs: logs, fixture: f,
		courier:   tokenFor(t, f, courierID, auth.UseErrands),
		rival:     tokenFor(t, f, rivalID, auth.UseErrands),
		requester: tokenFor(t, f, requesterID, auth.UseErrands),
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

func (h harness) post(t *testing.T, token, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/errands/"+id+"/accept", strings.NewReader(body))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	h.router.ServeHTTP(recorder, request)
	return recorder
}

func (h harness) accept(t *testing.T, token, id string, version int) *httptest.ResponseRecorder {
	t.Helper()
	return h.post(t, token, id, fmt.Sprintf(`{"expectedVersion":%d}`, version))
}

// withdraw records a withdrawal by courier at at: voluntary, or forced by
// the pickup deadline.
func (h harness) withdraw(t *testing.T, n int, courier string, at time.Time, forced bool) {
	t.Helper()
	h.store.Put(assignedTo(n, courier, at.Add(-time.Minute)))
	ev, actor := errand.EventWithdraw, errand.Actor{Type: errand.ActorCourier, ID: courier}
	if forced {
		ev, actor = errand.EventPickupDeadlinePassed, errand.SystemActor()
	}
	_, _, err := h.store.Fire(t.Context(), errandID(n), ev, actor, errand.EventInput{}, at)
	require.NoError(t, err)
}

func (h harness) rejections() []testfakes.OutboxRecord {
	return h.store.OutboxKinds(rejectedKind)
}

func requireError(t *testing.T, recorder *httptest.ResponseRecorder, status int, code apperror.Code) apperror.Error {
	t.Helper()
	require.Equal(t, status, recorder.Code, recorder.Body.String())
	var body apperror.Error
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	require.Equal(t, code, body.Code, recorder.Body.String())
	return body
}

func TestAccept(t *testing.T) {
	h := newHarness(t)
	h.store.Put(openErrand(1))
	settings := errand.DefaultSystemSettings()
	settings.Deadlines.PickupAfter = 30 * time.Minute
	settings.Deadlines.DeliveryAfter = 90 * time.Minute
	h.settings.Set(settings)

	recorder := h.accept(t, h.courier, errandID(1), 2)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.JSONEq(t, `{
		"errandId": "`+errandID(1)+`",
		"version": 3,
		"status": "ASSIGNED",
		"supplierId": "`+supplierID+`",
		"supplierVersionId": "`+versionID+`",
		"pickup": {"name": "Kopi Corner", "building": "COM1", "floor": "1",
			"locationDescription": "next to the lift", "latitude": 1.2949, "longitude": 103.7737},
		"deliveryBuilding": {"id": "`+buildingID+`", "name": "COM1"},
		"deliveryDetail": "Level 2 lounge",
		"items": [{"description": "iced latte", "quantity": 2}],
		"notes": "less ice",
		"reward": 10,
		"createdAt": "2026-03-02T08:40:00Z",
		"updatedAt": "2026-03-02T09:00:00Z",
		"acceptanceDeadline": "2026-03-02T10:00:00Z",
		"pickupDeadline": "2026-03-02T09:30:00Z",
		"deliveryDeadline": "2026-03-02T10:30:00Z",
		"viewerRole": "COURIER",
		"contactName": "`+contactName+`",
		"contactMobile": "`+contactMobile+`"
	}`, recorder.Body.String(), "deadlines come from settings; no requester identity")

	stored, ok := h.store.Errand(errandID(1))
	require.True(t, ok)
	assert.Equal(t, errand.StatusAssigned, stored.Status)
	assert.Equal(t, courierID, stored.CourierID)
	require.Len(t, h.store.OutboxKinds(errand.ErrandAccepted.Kind()), 1)
	assert.Empty(t, h.rejections())

	t.Run("a retry after the accept committed gets the same answer", func(t *testing.T) {
		retry := h.accept(t, h.courier, errandID(1), 2)
		require.Equal(t, http.StatusOK, retry.Code, retry.Body.String())
		assert.JSONEq(t, recorder.Body.String(), retry.Body.String())
		assert.Len(t, h.store.OutboxKinds(errand.ErrandAccepted.Kind()), 1, "nothing new is written")
		assert.Empty(t, h.rejections())
	})
}

func TestAcceptWinsOverPendingEdit(t *testing.T) {
	h := newHarness(t)
	e := openErrand(1)
	edit := e.Details
	edit.Reward = 25
	e.PendingEdit = &errand.PendingEdit{Details: edit, Key: errand.IdempotencyKey(e.ID, "adjust", 2), At: t0.Add(-time.Minute)}
	h.store.Put(e)

	recorder := h.accept(t, h.courier, errandID(1), 2)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())

	stored, _ := h.store.Errand(errandID(1))
	assert.Nil(t, stored.PendingEdit, "the pending edit is dropped (F3.4.2)")
	assert.Equal(t, 10, stored.Details.Reward, "the courier accepted the current reward")
	adjusts := h.store.OutboxKinds(errand.KindCreditAdjust)
	require.Len(t, adjusts, 1, "the reservation is adjusted back")
}

func TestAcceptEligibility(t *testing.T) {
	t.Run("the requester may not accept their own errand", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(openErrand(1))
		got := requireError(t, h.accept(t, h.requester, errandID(1), 2), http.StatusForbidden, apperror.NotEligible)
		assert.Contains(t, got.Message, "your own errand")
		assert.Empty(t, h.rejections(), "403s are not announced")
	})

	t.Run("a courier who withdrew may not accept again", func(t *testing.T) {
		h := newHarness(t)
		h.withdraw(t, 1, courierID, t0.Add(-2*time.Hour), false)
		// Withdrawn two hours ago, so not restricted; the errand is OPEN again.
		e, _ := h.store.Errand(errandID(1))
		requireError(t, h.accept(t, h.courier, errandID(1), e.Version), http.StatusForbidden, apperror.NotEligible)
		assert.Equal(t, http.StatusOK, h.accept(t, h.rival, errandID(1), e.Version).Code, "others still may")
	})

	t.Run("a forced withdrawal counts as a withdrawal from that errand", func(t *testing.T) {
		h := newHarness(t)
		h.withdraw(t, 1, courierID, t0.Add(-2*time.Hour), true)
		e, _ := h.store.Errand(errandID(1))
		require.Equal(t, errand.StatusOpen, e.Status)
		requireError(t, h.accept(t, h.courier, errandID(1), e.Version), http.StatusForbidden, apperror.NotEligible)
	})
}

func TestAcceptRestriction(t *testing.T) {
	setup := func(t *testing.T, forcedLast bool) harness {
		h := newHarness(t)
		h.store.Put(openErrand(1))
		h.withdraw(t, 11, courierID, t0.Add(-10*time.Minute), false)
		h.withdraw(t, 12, courierID, t0.Add(-5*time.Minute), false)
		h.withdraw(t, 13, courierID, t0.Add(-time.Minute), forcedLast)
		return h
	}

	t.Run("three withdrawals within the window restrict the courier", func(t *testing.T) {
		h := setup(t, false)
		requireError(t, h.accept(t, h.courier, errandID(1), 2), http.StatusForbidden, apperror.CourierRestricted)
		assert.Empty(t, h.rejections())
		assert.Equal(t, http.StatusOK, h.accept(t, h.rival, errandID(1), 2).Code, "only that courier is restricted")
	})

	t.Run("forced withdrawals count", func(t *testing.T) {
		h := setup(t, true)
		requireError(t, h.accept(t, h.courier, errandID(1), 2), http.StatusForbidden, apperror.CourierRestricted)
	})

	t.Run("the restriction lifts as withdrawals leave the window", func(t *testing.T) {
		h := setup(t, false)
		h.clock.Set(t0.Add(5 * time.Minute)) // the first is exactly 15 minutes old
		requireError(t, h.accept(t, h.courier, errandID(1), 2), http.StatusForbidden, apperror.CourierRestricted)
		h.clock.Set(t0.Add(5*time.Minute + time.Second))
		assert.Equal(t, http.StatusOK, h.accept(t, h.courier, errandID(1), 2).Code)
	})

	t.Run("the threshold comes from settings", func(t *testing.T) {
		h := setup(t, false)
		settings := errand.DefaultSystemSettings()
		settings.WithdrawalRestriction.Count = 4
		h.settings.Set(settings)
		assert.Equal(t, http.StatusOK, h.accept(t, h.courier, errandID(1), 2).Code)
	})
}

func TestAcceptConflicts(t *testing.T) {
	t.Run("a stale version is ERRAND_CHANGED and notifies once", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(openErrand(1))
		requireError(t, h.accept(t, h.courier, errandID(1), 1), http.StatusConflict, apperror.ErrandChanged)
		requireError(t, h.accept(t, h.courier, errandID(1), 1), http.StatusConflict, apperror.ErrandChanged)

		rejections := h.rejections()
		require.Len(t, rejections, 1, "a retry of the same request notifies once")
		var data errand.RejectionData
		require.NoError(t, json.Unmarshal(mustJSON(t, rejections[0].Payload), &data))
		assert.Equal(t, errand.RejectedAccept, data.Action)
		assert.Equal(t, apperror.ErrandChanged, data.Code)
		assert.Equal(t, courierID, data.ActorID)
		assert.Equal(t, 2, data.ErrandVersion)

		requireError(t, h.accept(t, h.rival, errandID(1), 1), http.StatusConflict, apperror.ErrandChanged)
		assert.Len(t, h.rejections(), 2, "another courier is notified separately")
	})

	t.Run("an errand someone else accepted is ERRAND_UNAVAILABLE", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(openErrand(1))
		require.Equal(t, http.StatusOK, h.accept(t, h.rival, errandID(1), 2).Code)
		requireError(t, h.accept(t, h.courier, errandID(1), 2), http.StatusConflict, apperror.ErrandUnavailable)
		require.Len(t, h.rejections(), 1)
		stored, _ := h.store.Errand(errandID(1))
		assert.Equal(t, rivalID, stored.CourierID)
	})

	t.Run("an errand past its acceptance deadline is ERRAND_UNAVAILABLE", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(openErrand(1))
		h.clock.Set(t0.Add(time.Hour))
		requireError(t, h.accept(t, h.courier, errandID(1), 2), http.StatusConflict, apperror.ErrandUnavailable)
		assert.Len(t, h.rejections(), 1)
	})

	t.Run("losing the compare-and-swap is announced", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(openErrand(1))
		h.store.FailNext("Apply", &apperror.Error{Code: apperror.ErrandUnavailable, Message: "errand is CANCELLED"})
		requireError(t, h.accept(t, h.courier, errandID(1), 2), http.StatusConflict, apperror.ErrandUnavailable)
		assert.Len(t, h.rejections(), 1)
	})

	t.Run("an errand not open yet is INVALID_TRANSITION and not announced", func(t *testing.T) {
		h := newHarness(t)
		e := openErrand(1)
		e.Status, e.Version = errand.StatusPendingReservation, 1
		h.store.Put(e)
		requireError(t, h.accept(t, h.courier, errandID(1), 1), http.StatusConflict, apperror.InvalidTransition)
		assert.Empty(t, h.rejections())
	})

	t.Run("a failed notification still answers 409", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(openErrand(1))
		h.store.FailNext("RecordRejection", errors.New("connection reset"))
		requireError(t, h.accept(t, h.courier, errandID(1), 1), http.StatusConflict, apperror.ErrandChanged)
		assert.Contains(t, h.logs.String(), "recording an accept rejection failed")
	})
}

func TestAcceptInvalidRequest(t *testing.T) {
	cases := []struct {
		name   string
		id     string
		body   string
		status int
		code   apperror.Code
		fields []apperror.Field
	}{
		{"malformed errand ID", "not-a-uuid", `{"expectedVersion":2}`, http.StatusBadRequest, apperror.InvalidArgument, nil},
		{"unknown errand", errandID(9), `{"expectedVersion":2}`, http.StatusNotFound, apperror.ErrandNotFound, nil},
		{"empty body", errandID(1), ``, http.StatusBadRequest, apperror.InvalidArgument, nil},
		{"not an object", errandID(1), `[2]`, http.StatusBadRequest, apperror.InvalidArgument, nil},
		{"missing version", errandID(1), `{}`, http.StatusBadRequest, apperror.ValidationFailed,
			[]apperror.Field{{Field: "expectedVersion", Message: "is required"}}},
		{"zero version", errandID(1), `{"expectedVersion":0}`, http.StatusBadRequest, apperror.ValidationFailed,
			[]apperror.Field{{Field: "expectedVersion", Message: "must be a positive integer"}}},
		{"string version", errandID(1), `{"expectedVersion":"2"}`, http.StatusBadRequest, apperror.ValidationFailed,
			[]apperror.Field{{Field: "expectedVersion", Message: "must be a positive integer"}}},
		{"identity in the body", errandID(1), `{"expectedVersion":2,"courierId":"auth0|someone"}`, http.StatusBadRequest,
			apperror.ValidationFailed, []apperror.Field{{Field: "courierId", Message: "is not allowed"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.store.Put(openErrand(1))
			got := requireError(t, h.post(t, h.courier, tc.id, tc.body), tc.status, tc.code)
			assert.Equal(t, tc.fields, got.Fields)
			stored, _ := h.store.Errand(errandID(1))
			assert.Equal(t, errand.StatusOpen, stored.Status)
			assert.Empty(t, h.rejections())
		})
	}
}

func TestAcceptDependencyFailures(t *testing.T) {
	t.Run("settings unreadable refuses the accept", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(openErrand(1))
		h.settings.FailNext("Get", errors.New("settings table unreadable"))
		requireError(t, h.accept(t, h.courier, errandID(1), 2), http.StatusInternalServerError, apperror.Internal)
		stored, _ := h.store.Errand(errandID(1))
		assert.Equal(t, errand.StatusOpen, stored.Status)
	})

	t.Run("history unreadable refuses the accept", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(openErrand(1))
		h.store.FailNext("CourierHistory", errors.New("connection reset"))
		requireError(t, h.accept(t, h.courier, errandID(1), 2), http.StatusInternalServerError, apperror.Internal)
	})

	t.Run("supplier-service down after the commit: the retry succeeds", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(openErrand(1))
		h.suppliers.FailNext("GetVersion", &apperror.Error{Code: apperror.DependencyUnavailable, Message: "down"})
		requireError(t, h.accept(t, h.courier, errandID(1), 2), http.StatusServiceUnavailable, apperror.DependencyUnavailable)
		stored, _ := h.store.Errand(errandID(1))
		require.Equal(t, errand.StatusAssigned, stored.Status, "the accept committed")

		retry := h.accept(t, h.courier, errandID(1), 2)
		assert.Equal(t, http.StatusOK, retry.Code, retry.Body.String())
		assert.Empty(t, h.rejections())
	})
}

func TestAcceptAuthorization(t *testing.T) {
	h := newHarness(t)
	h.store.Put(openErrand(1))
	requireError(t, h.accept(t, "", errandID(1), 2), http.StatusUnauthorized, apperror.Unauthenticated)
	requireError(t, h.accept(t, tokenFor(t, h.fixture, courierID, auth.ReviewErrands), errandID(1), 2),
		http.StatusForbidden, apperror.Forbidden)
	service := h.fixture.Token(t, h.fixture.ServiceClaims(auth.OrderAudience, []string{string(auth.UseErrands)}), "one")
	requireError(t, h.accept(t, service, errandID(1), 2), http.StatusForbidden, apperror.Forbidden)
	assert.Empty(t, h.rejections())
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	if raw, ok := v.(json.RawMessage); ok {
		return raw
	}
	if raw, ok := v.([]byte); ok {
		return raw
	}
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return raw
}
