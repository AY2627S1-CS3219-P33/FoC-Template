package requester_test

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
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand/requester"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/httpapi"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/testfakes"
)

const (
	requesterID = "auth0|requester-1"
	courierID   = "auth0|courier-1"
	strangerID  = "auth0|stranger-1"

	supplierID     = "0b8e6a52-3c1d-4f7e-8a9b-1c2d3e4f5a6b"
	versionID      = "5d2c1b0a-9f8e-4d7c-8b6a-5f4e3d2c1b0a"
	otherSupplier  = "1c9f7b63-4d2e-4a8f-9bac-2d3e4f5a6b7c"
	otherVersion   = "6e3d2c1b-0a9f-4e8d-9c7b-6a5f4e3d2c1b"
	buildingID     = "7a6b5c4d-3e2f-4a1b-9c8d-7e6f5a4b3c2d"
	otherBuilding  = "8b7c6d5e-4f3a-4b2c-8d9e-8f7a6b5c4d3e"
	retiredBuildng = "9c8d7e6f-5a4b-4c3d-9e0f-9a8b7c6d5e4f"
	contactName    = "Alex Tan"
	contactMobile  = "+65 9123 4567"

	cancelRejected = "event.errand.cancel_rejected"
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

// withPendingEdit is openErrand(n) with a reward change to reward waiting
// for credit-service.
func withPendingEdit(n, reward int) errand.Errand {
	e := openErrand(n)
	edit := e.Details
	edit.Reward = reward
	e.PendingEdit = &errand.PendingEdit{Details: edit, Key: errand.IdempotencyKey(e.ID, "adjust", 2), At: t0.Add(-time.Minute)}
	return e
}

// deliveredErrand is openErrand(n) delivered by courierID at version 5.
func deliveredErrand(n int) errand.Errand {
	e := openErrand(n)
	e.Status, e.Version, e.CourierID = errand.StatusDelivered, 5, courierID
	e.PickupDeadline, e.DeliveryDeadline = ptr(t0.Add(-time.Hour)), ptr(t0.Add(time.Hour))
	e.DeliveredAt, e.VerificationDeadline = ptr(t0.Add(-5*time.Minute)), ptr(t0.Add(25*time.Minute))
	return e
}

type harness struct {
	router    http.Handler
	store     *testfakes.FakeStore
	suppliers *testfakes.FakeSupplierGate
	clock     *testfakes.FakeClock
	logs      *strings.Builder
	fixture   *authtest.Fixture
	requester string
	stranger  string
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
	suppliers.AddSupplier(errand.SupplierVersion{
		SupplierID: otherSupplier, VersionID: otherVersion, Name: "Fine Food",
		Building: "Town Plaza", Floor: "1", LocationDescription: "food court",
		Latitude: 1.3045, Longitude: 103.7731,
	})
	suppliers.SetBuildings(
		errand.Building{ID: buildingID, Name: "COM1"},
		errand.Building{ID: otherBuilding, Name: "COM3"},
		errand.Building{ID: retiredBuildng, Name: "S16", Retired: true},
	)
	clock := testfakes.NewFakeClock(t0)
	handler := requester.NewHandler(requester.Dependencies{
		Store: store, Suppliers: suppliers, Clock: clock, Logger: logger,
	}, auth.NewMiddleware(verifier))
	return harness{
		router: httpapi.NewRouter(httpapi.Dependencies{Logger: logger}, handler),
		store:  store, suppliers: suppliers, clock: clock, logs: logs, fixture: f,
		requester: tokenFor(t, f, requesterID, auth.UseErrands),
		stranger:  tokenFor(t, f, strangerID, auth.UseErrands),
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

func (h harness) do(t *testing.T, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	h.router.ServeHTTP(recorder, request)
	return recorder
}

func (h harness) patch(t *testing.T, token, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	return h.do(t, http.MethodPatch, "/errands/"+id, token, body)
}

// act posts a VersionedAction to /errands/{id}/{action}.
func (h harness) act(t *testing.T, token, action, id string, version int) *httptest.ResponseRecorder {
	t.Helper()
	return h.do(t, http.MethodPost, "/errands/"+id+"/"+action, token, fmt.Sprintf(`{"expectedVersion":%d}`, version))
}

func (h harness) dispute(t *testing.T, id string, version int, reason string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"expectedVersion": version, "reason": reason})
	return h.do(t, http.MethodPost, "/errands/"+id+"/dispute", h.requester, string(body))
}

func (h harness) stored(t *testing.T, n int) errand.Errand {
	t.Helper()
	e, ok := h.store.Errand(errandID(n))
	require.True(t, ok)
	return e
}

func requireError(t *testing.T, recorder *httptest.ResponseRecorder, status int, code apperror.Code) apperror.Error {
	t.Helper()
	require.Equal(t, status, recorder.Code, recorder.Body.String())
	var body apperror.Error
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	require.Equal(t, code, body.Code, recorder.Body.String())
	return body
}

// view decodes a RequesterErrandView far enough for the assertions.
type view struct {
	Version           int           `json:"version"`
	Status            errand.Status `json:"status"`
	SupplierID        string        `json:"supplierId"`
	SupplierVersionID string        `json:"supplierVersionId"`
	Pickup            struct {
		Name string `json:"name"`
	} `json:"pickup"`
	DeliveryBuilding struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"deliveryBuilding"`
	Notes        string              `json:"notes"`
	Reward       int                 `json:"reward"`
	ContactName  string              `json:"contactName"`
	ViewerRole   string              `json:"viewerRole"`
	ReviewReason errand.ReviewReason `json:"reviewReason"`
	PendingEdit  *struct {
		SupplierID string `json:"supplierId"`
		Pickup     struct {
			Name string `json:"name"`
		} `json:"pickup"`
		Notes       string    `json:"notes"`
		Reward      int       `json:"reward"`
		RequestedAt time.Time `json:"requestedAt"`
	} `json:"pendingEdit"`
}

func decodeView(t *testing.T, recorder *httptest.ResponseRecorder) view {
	t.Helper()
	var v view
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &v), recorder.Body.String())
	return v
}

func payload[T any](t *testing.T, record testfakes.OutboxRecord) T {
	t.Helper()
	raw, err := json.Marshal(record.Payload)
	require.NoError(t, err)
	var v T
	require.NoError(t, json.Unmarshal(raw, &v))
	return v
}

// ---------------------------------------------------------------------------
// Edit
// ---------------------------------------------------------------------------

func TestEditWithoutRewardChangeIsAppliedAtOnce(t *testing.T) {
	h := newHarness(t)
	h.store.Put(openErrand(1))

	recorder := h.patch(t, h.requester, errandID(1), `{"expectedVersion":2,"notes":"no ice","contactName":"Sam Lee"}`)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	got := decodeView(t, recorder)
	assert.Equal(t, 3, got.Version, "an applied edit is a new version")
	assert.Equal(t, errand.StatusOpen, got.Status)
	assert.Equal(t, "no ice", got.Notes)
	assert.Equal(t, "Sam Lee", got.ContactName)
	assert.Equal(t, 10, got.Reward)
	assert.Equal(t, "REQUESTER", got.ViewerRole)
	assert.Nil(t, got.PendingEdit)

	stored := h.stored(t, 1)
	assert.Equal(t, "no ice", stored.Details.Notes)
	assert.Equal(t, "Level 2 lounge", stored.Details.DeliveryDetail, "omitted fields keep their value")
	assert.Len(t, h.store.OutboxKinds(errand.ErrandEdited.Kind()), 1)
	assert.Empty(t, h.store.OutboxKinds(errand.KindCreditAdjust), "no credit change")
}

func TestEditRewardChangeWaitsForCreditService(t *testing.T) {
	setup := func(t *testing.T) (harness, *httptest.ResponseRecorder) {
		h := newHarness(t)
		h.store.Put(openErrand(1))
		return h, h.patch(t, h.requester, errandID(1), `{"expectedVersion":2,"reward":25,"notes":"no ice"}`)
	}

	h, recorder := setup(t)
	require.Equal(t, http.StatusAccepted, recorder.Code, recorder.Body.String())
	got := decodeView(t, recorder)
	assert.Equal(t, 2, got.Version, "the version is kept, so couriers can still accept the current details")
	assert.Equal(t, 10, got.Reward)
	assert.Equal(t, "less ice", got.Notes, "nothing is applied yet, not even the other fields")
	require.NotNil(t, got.PendingEdit)
	assert.Equal(t, 25, got.PendingEdit.Reward)
	assert.Equal(t, "no ice", got.PendingEdit.Notes)
	assert.Equal(t, "Kopi Corner", got.PendingEdit.Pickup.Name)
	assert.Equal(t, t0, got.PendingEdit.RequestedAt)

	stored := h.stored(t, 1)
	assert.Equal(t, 10, stored.Details.Reward)
	require.NotNil(t, stored.PendingEdit)
	adjusts := h.store.OutboxKinds(errand.KindCreditAdjust)
	require.Len(t, adjusts, 1)
	assert.Equal(t, errandID(1)+":adjust:2", adjusts[0].IdempotencyKey)
	cmd := payload[errand.CreditCommand](t, adjusts[0])
	assert.Equal(t, 25, cmd.NewAmount, "the adjust carries the absolute amount")
	assert.Equal(t, errand.AdjustSeq(2), cmd.Seq)
	assert.Len(t, h.store.OutboxKinds(errand.ErrandEditRequested.Kind()), 1)
	assert.Empty(t, h.store.OutboxKinds(errand.ErrandEdited.Kind()))

	t.Run("another edit while it is pending is EDIT_PENDING", func(t *testing.T) {
		requireError(t, h.patch(t, h.requester, errandID(1), `{"expectedVersion":2,"notes":"extra ice"}`),
			http.StatusConflict, apperror.EditPending)
		assert.Len(t, h.store.OutboxKinds(errand.KindCreditAdjust), 1)
	})

	t.Run("credit-service confirms: the whole edit is applied at the next version", func(t *testing.T) {
		h, _ := setup(t)
		e, _, err := h.store.Fire(t.Context(), errandID(1), errand.EventEditConfirmed, errand.SystemActor(),
			errand.EventInput{EditKey: errandID(1) + ":adjust:2"}, t0.Add(time.Second))
		require.NoError(t, err)
		assert.Equal(t, 3, e.Version)
		assert.Equal(t, 25, e.Details.Reward)
		assert.Equal(t, "no ice", e.Details.Notes)
		assert.Nil(t, e.PendingEdit)
	})

	t.Run("credit-service refuses: nothing is applied and the version still moves", func(t *testing.T) {
		h, _ := setup(t)
		e, _, err := h.store.Fire(t.Context(), errandID(1), errand.EventEditRefused, errand.SystemActor(),
			errand.EventInput{EditKey: errandID(1) + ":adjust:2"}, t0.Add(time.Second))
		require.NoError(t, err)
		assert.Equal(t, 3, e.Version)
		assert.Equal(t, 10, e.Details.Reward)
		assert.Equal(t, "less ice", e.Details.Notes)
		assert.Nil(t, e.PendingEdit)
	})
}

func TestEditSupplierAndBuilding(t *testing.T) {
	t.Run("a new supplier is resolved to its current version and a new building is snapshotted", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(openErrand(1))
		body := fmt.Sprintf(`{"expectedVersion":2,"supplierId":%q,"deliveryBuildingId":%q}`,
			strings.ToUpper(otherSupplier), otherBuilding)
		recorder := h.patch(t, h.requester, errandID(1), body)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		got := decodeView(t, recorder)
		assert.Equal(t, otherSupplier, got.SupplierID)
		assert.Equal(t, otherVersion, got.SupplierVersionID)
		assert.Equal(t, "Fine Food", got.Pickup.Name)
		assert.Equal(t, otherBuilding, got.DeliveryBuilding.ID)
		assert.Equal(t, "COM3", got.DeliveryBuilding.Name)
	})

	t.Run("a pending edit shows the new supplier's pickup", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(openErrand(1))
		recorder := h.patch(t, h.requester, errandID(1),
			fmt.Sprintf(`{"expectedVersion":2,"supplierId":%q,"reward":12}`, otherSupplier))
		require.Equal(t, http.StatusAccepted, recorder.Code, recorder.Body.String())
		got := decodeView(t, recorder)
		assert.Equal(t, "Kopi Corner", got.Pickup.Name)
		require.NotNil(t, got.PendingEdit)
		assert.Equal(t, "Fine Food", got.PendingEdit.Pickup.Name)
	})

	t.Run("the same supplier keeps its stored version", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(openErrand(1))
		h.suppliers.AddSupplier(errand.SupplierVersion{SupplierID: supplierID, VersionID: otherVersion, Name: "Kopi Corner v2"})
		recorder := h.patch(t, h.requester, errandID(1), fmt.Sprintf(`{"expectedVersion":2,"supplierId":%q}`, supplierID))
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.Equal(t, versionID, decodeView(t, recorder).SupplierVersionID)
	})

	t.Run("an unavailable supplier is SUPPLIER_UNAVAILABLE", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(openErrand(1))
		h.suppliers.SetUnavailable(otherSupplier)
		requireError(t, h.patch(t, h.requester, errandID(1), fmt.Sprintf(`{"expectedVersion":2,"supplierId":%q}`, otherSupplier)),
			http.StatusUnprocessableEntity, apperror.SupplierUnavailable)
		assert.Equal(t, 2, h.stored(t, 1).Version)
	})

	t.Run("a supplier being deleted is SUPPLIER_DELETION_IN_PROGRESS", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(openErrand(1))
		h.store.Fence(otherSupplier)
		requireError(t, h.patch(t, h.requester, errandID(1), fmt.Sprintf(`{"expectedVersion":2,"supplierId":%q}`, otherSupplier)),
			http.StatusConflict, apperror.SupplierDeletionInProgress)
		assert.Equal(t, supplierID, h.stored(t, 1).Details.SupplierID)
	})

	t.Run("a retired or unknown building is refused", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(openErrand(1))
		got := requireError(t, h.patch(t, h.requester, errandID(1), fmt.Sprintf(`{"expectedVersion":2,"deliveryBuildingId":%q}`, retiredBuildng)),
			http.StatusBadRequest, apperror.ValidationFailed)
		assert.Equal(t, []apperror.Field{{Field: "deliveryBuildingId", Message: "must not be a retired building"}}, got.Fields)
		got = requireError(t, h.patch(t, h.requester, errandID(1), fmt.Sprintf(`{"expectedVersion":2,"deliveryBuildingId":%q}`, errandID(99))),
			http.StatusBadRequest, apperror.ValidationFailed)
		assert.Equal(t, []apperror.Field{{Field: "deliveryBuildingId", Message: "must be an existing building"}}, got.Fields)
	})
}

func TestEditAcceptanceDeadline(t *testing.T) {
	t.Run("a changed deadline must be 10 minutes to 24 hours away", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(openErrand(1))
		body := fmt.Sprintf(`{"expectedVersion":2,"acceptanceDeadline":%q}`, t0.Add(5*time.Minute).Format(time.RFC3339))
		got := requireError(t, h.patch(t, h.requester, errandID(1), body), http.StatusBadRequest, apperror.ValidationFailed)
		require.Len(t, got.Fields, 1)
		assert.Equal(t, "acceptanceDeadline", got.Fields[0].Field)

		body = fmt.Sprintf(`{"expectedVersion":2,"acceptanceDeadline":%q}`, t0.Add(3*time.Hour).Format(time.RFC3339))
		recorder := h.patch(t, h.requester, errandID(1), body)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.Equal(t, t0.Add(3*time.Hour), h.stored(t, 1).Details.AcceptanceDeadline)
	})

	t.Run("an unchanged deadline under 10 minutes away does not block other edits", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(openErrand(1))
		h.clock.Set(t0.Add(55 * time.Minute))
		recorder := h.patch(t, h.requester, errandID(1), `{"expectedVersion":2,"notes":"no ice"}`)
		assert.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	})
}

func TestEditInvalidRequest(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		status int
		code   apperror.Code
		fields []apperror.Field
	}{
		{"empty body", ``, http.StatusBadRequest, apperror.InvalidArgument, nil},
		{"not an object", `[1]`, http.StatusBadRequest, apperror.InvalidArgument, nil},
		{"nothing to change", `{"expectedVersion":2}`, http.StatusBadRequest, apperror.InvalidArgument, nil},
		{"only nulls", `{"expectedVersion":2,"notes":null}`, http.StatusBadRequest, apperror.InvalidArgument, nil},
		{"missing version", `{"notes":"x"}`, http.StatusBadRequest, apperror.ValidationFailed,
			[]apperror.Field{{Field: "expectedVersion", Message: "is required"}}},
		{"every invalid field at once",
			`{"expectedVersion":2,"deliveryDetail":"  ","reward":0,"items":[],"contactMobile":"\u0007","supplierId":"x","courierId":"y"}`,
			http.StatusBadRequest, apperror.ValidationFailed, []apperror.Field{
				{Field: "supplierId", Message: "must be a UUID"},
				{Field: "deliveryDetail", Message: "must not be blank"},
				{Field: "items", Message: "must list at least one item"},
				{Field: "contactMobile", Message: "must not contain control characters"},
				{Field: "reward", Message: "must be a positive integer"},
				{Field: "courierId", Message: "is not allowed"},
			}},
		{"blank contact name", `{"expectedVersion":2,"contactName":""}`, http.StatusBadRequest, apperror.ValidationFailed,
			[]apperror.Field{{Field: "contactName", Message: "must not be blank"}}},
		{"blank notes are allowed", `{"expectedVersion":2,"notes":""}`, http.StatusOK, "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.store.Put(openErrand(1))
			recorder := h.patch(t, h.requester, errandID(1), tc.body)
			if tc.code == "" {
				assert.Equal(t, tc.status, recorder.Code, recorder.Body.String())
				return
			}
			got := requireError(t, recorder, tc.status, tc.code)
			assert.Equal(t, tc.fields, got.Fields)
			assert.Equal(t, 2, h.stored(t, 1).Version)
		})
	}
}

func TestEditConflicts(t *testing.T) {
	t.Run("a stale version is ERRAND_CHANGED", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(openErrand(1))
		requireError(t, h.patch(t, h.requester, errandID(1), `{"expectedVersion":1,"notes":"x"}`), http.StatusConflict, apperror.ErrandChanged)
	})

	t.Run("an errand a courier accepted is ERRAND_UNAVAILABLE", func(t *testing.T) {
		h := newHarness(t)
		e := openErrand(1)
		e.Status, e.Version, e.CourierID = errand.StatusAssigned, 3, courierID
		h.store.Put(e)
		requireError(t, h.patch(t, h.requester, errandID(1), `{"expectedVersion":3,"reward":20}`), http.StatusConflict, apperror.ErrandUnavailable)
		assert.Equal(t, 10, h.stored(t, 1).Details.Reward)
	})

	t.Run("an errand still pending reservation is INVALID_TRANSITION", func(t *testing.T) {
		h := newHarness(t)
		e := openErrand(1)
		e.Status, e.Version = errand.StatusPendingReservation, 1
		h.store.Put(e)
		requireError(t, h.patch(t, h.requester, errandID(1), `{"expectedVersion":1,"notes":"x"}`), http.StatusConflict, apperror.InvalidTransition)
	})

	t.Run("refusals call no supplier-service and are not announced", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(withPendingEdit(1, 20))
		body := fmt.Sprintf(`{"expectedVersion":2,"supplierId":%q}`, otherSupplier)
		requireError(t, h.patch(t, h.requester, errandID(1), body), http.StatusConflict, apperror.EditPending)
		assert.Empty(t, h.suppliers.Calls())
		assert.Empty(t, h.store.OutboxKinds(cancelRejected))
	})

	t.Run("someone else's errand is FORBIDDEN", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(openErrand(1))
		requireError(t, h.patch(t, h.stranger, errandID(1), `{"expectedVersion":2,"reward":1}`), http.StatusForbidden, apperror.Forbidden)
		assert.Equal(t, 10, h.stored(t, 1).Details.Reward)
	})
}

// ---------------------------------------------------------------------------
// Cancel
// ---------------------------------------------------------------------------

func TestCancel(t *testing.T) {
	h := newHarness(t)
	h.store.Put(openErrand(1))

	recorder := h.act(t, h.requester, "cancel", errandID(1), 2)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	got := decodeView(t, recorder)
	assert.Equal(t, errand.StatusCancelled, got.Status)
	assert.Equal(t, 3, got.Version)
	require.Len(t, h.store.OutboxKinds(errand.KindCreditRelease), 1, "the reservation is released (F4.5.1)")
	require.Len(t, h.store.OutboxKinds(errand.ErrandCancelled.Kind()), 1)

	t.Run("a retry after the cancel committed gets the same answer", func(t *testing.T) {
		retry := h.act(t, h.requester, "cancel", errandID(1), 2)
		require.Equal(t, http.StatusOK, retry.Code, retry.Body.String())
		assert.JSONEq(t, recorder.Body.String(), retry.Body.String())
		assert.Len(t, h.store.OutboxKinds(errand.KindCreditRelease), 1, "nothing new is written")
		assert.Empty(t, h.store.OutboxKinds(cancelRejected))
	})
}

func TestCancelDropsPendingEdit(t *testing.T) {
	h := newHarness(t)
	h.store.Put(withPendingEdit(1, 25))
	recorder := h.act(t, h.requester, "cancel", errandID(1), 2)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Nil(t, decodeView(t, recorder).PendingEdit)

	stored := h.stored(t, 1)
	assert.Nil(t, stored.PendingEdit)
	assert.Len(t, h.store.OutboxKinds(errand.KindCreditRelease), 1, "the release returns the whole reservation")
	assert.Empty(t, h.store.OutboxKinds(errand.KindCreditAdjust), "so no reverting adjust is needed")
}

func TestCancelConflicts(t *testing.T) {
	t.Run("an errand a courier accepted is ERRAND_UNAVAILABLE and notifies once", func(t *testing.T) {
		h := newHarness(t)
		e := openErrand(1)
		e.Status, e.Version, e.CourierID = errand.StatusAssigned, 3, courierID
		h.store.Put(e)
		requireError(t, h.act(t, h.requester, "cancel", errandID(1), 2), http.StatusConflict, apperror.ErrandUnavailable)
		requireError(t, h.act(t, h.requester, "cancel", errandID(1), 2), http.StatusConflict, apperror.ErrandUnavailable)

		rejections := h.store.OutboxKinds(cancelRejected)
		require.Len(t, rejections, 1, "a retry of the same request notifies once")
		data := payload[errand.RejectionData](t, rejections[0])
		assert.Equal(t, errand.RejectedCancel, data.Action)
		assert.Equal(t, apperror.ErrandUnavailable, data.Code)
		assert.Equal(t, errand.ActorRequester, data.ActorType)
		assert.Equal(t, requesterID, data.ActorID)
		assert.Equal(t, errand.StatusAssigned, data.Status)
		assert.Empty(t, h.store.OutboxKinds(errand.KindCreditRelease))
	})

	t.Run("a stale version is ERRAND_CHANGED and announced", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(openErrand(1))
		requireError(t, h.act(t, h.requester, "cancel", errandID(1), 1), http.StatusConflict, apperror.ErrandChanged)
		assert.Len(t, h.store.OutboxKinds(cancelRejected), 1)
		assert.Equal(t, errand.StatusOpen, h.stored(t, 1).Status)
	})

	t.Run("an errand pending reservation is INVALID_TRANSITION and announced", func(t *testing.T) {
		h := newHarness(t)
		e := openErrand(1)
		e.Status, e.Version = errand.StatusPendingReservation, 1
		h.store.Put(e)
		requireError(t, h.act(t, h.requester, "cancel", errandID(1), 1), http.StatusConflict, apperror.InvalidTransition)
		assert.Len(t, h.store.OutboxKinds(cancelRejected), 1)
	})

	t.Run("losing the compare-and-swap is announced", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(openErrand(1))
		h.store.FailNext("Apply", &apperror.Error{Code: apperror.ErrandUnavailable, Message: "errand is ASSIGNED"})
		requireError(t, h.act(t, h.requester, "cancel", errandID(1), 2), http.StatusConflict, apperror.ErrandUnavailable)
		assert.Len(t, h.store.OutboxKinds(cancelRejected), 1)
	})

	t.Run("someone else's errand is FORBIDDEN and not announced", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(openErrand(1))
		requireError(t, h.act(t, h.stranger, "cancel", errandID(1), 2), http.StatusForbidden, apperror.Forbidden)
		assert.Empty(t, h.store.OutboxKinds(cancelRejected))
		assert.Equal(t, errand.StatusOpen, h.stored(t, 1).Status)
	})

	t.Run("a failed notification still answers 409", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(openErrand(1))
		h.store.FailNext("RecordRejection", errors.New("connection reset"))
		requireError(t, h.act(t, h.requester, "cancel", errandID(1), 1), http.StatusConflict, apperror.ErrandChanged)
		assert.Contains(t, h.logs.String(), "recording a cancel rejection failed")
	})
}

// ---------------------------------------------------------------------------
// Confirm and dispute
// ---------------------------------------------------------------------------

func TestConfirm(t *testing.T) {
	h := newHarness(t)
	h.store.Put(deliveredErrand(1))

	recorder := h.act(t, h.requester, "confirm", errandID(1), 5)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	got := decodeView(t, recorder)
	assert.Equal(t, errand.StatusCompleted, got.Status)
	assert.Equal(t, 6, got.Version)
	transfers := h.store.OutboxKinds(errand.KindCreditTransfer)
	require.Len(t, transfers, 1, "the reward goes to the courier (F4.2.2)")
	cmd := payload[errand.CreditCommand](t, transfers[0])
	assert.Equal(t, courierID, cmd.CourierID)
	assert.Equal(t, 10, cmd.Amount)

	t.Run("a repeated confirm is a no-op whatever expectedVersion says", func(t *testing.T) {
		for _, version := range []int{5, 6, 1} {
			repeat := h.act(t, h.requester, "confirm", errandID(1), version)
			require.Equal(t, http.StatusOK, repeat.Code, repeat.Body.String())
			assert.Equal(t, errand.StatusCompleted, decodeView(t, repeat).Status)
		}
		assert.Len(t, h.store.OutboxKinds(errand.KindCreditTransfer), 1)
	})

	t.Run("someone else may not confirm", func(t *testing.T) {
		requireError(t, h.act(t, h.stranger, "confirm", errandID(1), 6), http.StatusForbidden, apperror.Forbidden)
	})
}

func TestConfirmConflicts(t *testing.T) {
	t.Run("before delivery is INVALID_TRANSITION", func(t *testing.T) {
		h := newHarness(t)
		e := deliveredErrand(1)
		e.Status, e.Version, e.DeliveredAt, e.VerificationDeadline = errand.StatusPickedUp, 4, nil, nil
		h.store.Put(e)
		requireError(t, h.act(t, h.requester, "confirm", errandID(1), 4), http.StatusConflict, apperror.InvalidTransition)
		assert.Empty(t, h.store.OutboxKinds(errand.KindCreditTransfer))
		assert.Empty(t, h.store.Outbox(), "confirm refusals are not announced")
	})

	t.Run("a disputed errand is ERRAND_UNAVAILABLE", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(deliveredErrand(1))
		require.Equal(t, http.StatusOK, h.dispute(t, errandID(1), 5, "wrong order").Code)
		requireError(t, h.act(t, h.requester, "confirm", errandID(1), 6), http.StatusConflict, apperror.ErrandUnavailable)
	})

	t.Run("a stale version is ERRAND_CHANGED", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(deliveredErrand(1))
		requireError(t, h.act(t, h.requester, "confirm", errandID(1), 4), http.StatusConflict, apperror.ErrandChanged)
	})
}

func TestDispute(t *testing.T) {
	h := newHarness(t)
	h.store.Put(deliveredErrand(1))

	recorder := h.dispute(t, errandID(1), 5, "two lattes, got one")
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	got := decodeView(t, recorder)
	assert.Equal(t, errand.StatusReviewRequired, got.Status)
	assert.Equal(t, errand.ReviewRequesterDispute, got.ReviewReason)
	assert.Equal(t, 6, got.Version)

	reviews := h.store.Reviews()
	require.Len(t, reviews, 1)
	assert.Equal(t, errand.ReviewRequesterDispute, reviews[0].Reason)
	assert.Equal(t, requesterID, reviews[0].ReporterID)
	assert.Equal(t, "two lattes, got one", reviews[0].Note, "the reason becomes the review's note")
	assert.Empty(t, h.store.OutboxKinds(errand.KindCreditTransfer), "the reservation stays held")
	assert.Empty(t, h.store.OutboxKinds(errand.KindCreditRelease))

	t.Run("a retry after the dispute committed gets the same answer", func(t *testing.T) {
		retry := h.dispute(t, errandID(1), 5, "two lattes, got one")
		require.Equal(t, http.StatusOK, retry.Code, retry.Body.String())
		assert.JSONEq(t, recorder.Body.String(), retry.Body.String())
		assert.Len(t, h.store.Reviews(), 1)
	})
}

func TestDisputeInvalidRequest(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		fields []apperror.Field
	}{
		{"missing reason", `{"expectedVersion":5}`, []apperror.Field{{Field: "reason", Message: "is required"}}},
		{"blank reason", `{"expectedVersion":5,"reason":"   "}`, []apperror.Field{{Field: "reason", Message: "must not be blank"}}},
		{"long reason", `{"expectedVersion":5,"reason":"` + strings.Repeat("x", 501) + `"}`,
			[]apperror.Field{{Field: "reason", Message: "must be at most 500 characters"}}},
		{"everything wrong", `{"reason":7,"extra":true}`, []apperror.Field{
			{Field: "expectedVersion", Message: "is required"},
			{Field: "reason", Message: "must be a string"},
			{Field: "extra", Message: "is not allowed"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.store.Put(deliveredErrand(1))
			recorder := h.do(t, http.MethodPost, "/errands/"+errandID(1)+"/dispute", h.requester, tc.body)
			got := requireError(t, recorder, http.StatusBadRequest, apperror.ValidationFailed)
			assert.Equal(t, tc.fields, got.Fields)
			assert.Equal(t, errand.StatusDelivered, h.stored(t, 1).Status)
		})
	}
}

// ---------------------------------------------------------------------------
// Shared behaviour
// ---------------------------------------------------------------------------

func TestInvalidPathAndBody(t *testing.T) {
	h := newHarness(t)
	h.store.Put(openErrand(1))
	requireError(t, h.act(t, h.requester, "cancel", "not-a-uuid", 2), http.StatusBadRequest, apperror.InvalidArgument)
	requireError(t, h.patch(t, h.requester, "not-a-uuid", `{"expectedVersion":2,"notes":"x"}`), http.StatusBadRequest, apperror.InvalidArgument)
	requireError(t, h.act(t, h.requester, "cancel", errandID(9), 2), http.StatusNotFound, apperror.ErrandNotFound)
	requireError(t, h.patch(t, h.requester, errandID(9), `{"expectedVersion":2,"notes":"x"}`), http.StatusNotFound, apperror.ErrandNotFound)
	got := requireError(t, h.do(t, http.MethodPost, "/errands/"+errandID(1)+"/cancel", h.requester, `{"expectedVersion":2,"reason":"x"}`),
		http.StatusBadRequest, apperror.ValidationFailed)
	assert.Equal(t, []apperror.Field{{Field: "reason", Message: "is not allowed"}}, got.Fields)
	assert.Equal(t, errand.StatusOpen, h.stored(t, 1).Status)
}

func TestSupplierServiceDownWritesNothing(t *testing.T) {
	down := &apperror.Error{Code: apperror.DependencyUnavailable, Message: "down"}

	t.Run("cancel", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(openErrand(1))
		h.suppliers.FailNext("GetVersion", down)
		requireError(t, h.act(t, h.requester, "cancel", errandID(1), 2), http.StatusServiceUnavailable, apperror.DependencyUnavailable)
		assert.Equal(t, errand.StatusOpen, h.stored(t, 1).Status)
		assert.Empty(t, h.store.Outbox())
	})

	t.Run("edit", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(openErrand(1))
		h.suppliers.FailNext("ListBuildings", down)
		requireError(t, h.patch(t, h.requester, errandID(1), fmt.Sprintf(`{"expectedVersion":2,"deliveryBuildingId":%q}`, otherBuilding)),
			http.StatusServiceUnavailable, apperror.DependencyUnavailable)
		h.suppliers.FailNext("GetVersion", down)
		requireError(t, h.patch(t, h.requester, errandID(1), `{"expectedVersion":2,"reward":30}`),
			http.StatusServiceUnavailable, apperror.DependencyUnavailable)
		stored := h.stored(t, 1)
		assert.Equal(t, buildingID, stored.Details.DeliveryBuildingID)
		assert.Nil(t, stored.PendingEdit)
		assert.Empty(t, h.store.Outbox())
	})
}

func TestAuthorization(t *testing.T) {
	h := newHarness(t)
	h.store.Put(openErrand(1))
	requireError(t, h.act(t, "", "cancel", errandID(1), 2), http.StatusUnauthorized, apperror.Unauthenticated)
	requireError(t, h.patch(t, "", errandID(1), `{"expectedVersion":2,"notes":"x"}`), http.StatusUnauthorized, apperror.Unauthenticated)
	reviewer := tokenFor(t, h.fixture, requesterID, auth.ReviewErrands)
	requireError(t, h.act(t, reviewer, "cancel", errandID(1), 2), http.StatusForbidden, apperror.Forbidden)
	service := h.fixture.Token(t, h.fixture.ServiceClaims(auth.OrderAudience, []string{string(auth.UseErrands)}), "one")
	requireError(t, h.act(t, service, "cancel", errandID(1), 2), http.StatusForbidden, apperror.Forbidden)
	assert.Equal(t, errand.StatusOpen, h.stored(t, 1).Status)
	assert.Empty(t, h.store.Outbox())
}
