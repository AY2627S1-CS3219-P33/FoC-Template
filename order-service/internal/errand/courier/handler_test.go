package courier_test

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
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand/courier"
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

	rejectedKind = "event.errand.pickup_rejected"
)

var t0 = time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)

func errandID(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) }

func ptr[T any](v T) *T { return &v }

// assigned is ASSIGNED to courierID at version 3, with an hour left to
// accept it and 30 minutes left to pick it up.
func assigned(n int) errand.Errand {
	return errand.Errand{
		ID:          errandID(n),
		Status:      errand.StatusAssigned,
		Version:     3,
		RequesterID: requesterID,
		CourierID:   courierID,
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
		PickupDeadline:   ptr(t0.Add(30 * time.Minute)),
		DeliveryDeadline: ptr(t0.Add(90 * time.Minute)),
		CreatedAt:        t0.Add(-20 * time.Minute),
		UpdatedAt:        t0.Add(-10 * time.Minute),
	}
}

// pickedUp is assigned(n) PICKED_UP at version 4.
func pickedUp(n int) errand.Errand {
	e := assigned(n)
	e.Status, e.Version = errand.StatusPickedUp, 4
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
	handler := courier.NewHandler(courier.Dependencies{
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

func (h harness) post(t *testing.T, token, id, action, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/errands/"+id+"/"+action, strings.NewReader(body))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	h.router.ServeHTTP(recorder, request)
	return recorder
}

func (h harness) act(t *testing.T, token, id, action string, version int) *httptest.ResponseRecorder {
	t.Helper()
	return h.post(t, token, id, action, fmt.Sprintf(`{"expectedVersion":%d}`, version))
}

func (h harness) report(t *testing.T, token, id string, version int, reason string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{"expectedVersion": version, "reason": reason})
	require.NoError(t, err)
	return h.post(t, token, id, "report-issue", string(body))
}

func (h harness) stored(t *testing.T, n int) errand.Errand {
	t.Helper()
	e, ok := h.store.Errand(errandID(n))
	require.True(t, ok)
	return e
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

func decode(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var body map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	return body
}

func rejectionData(t *testing.T, row testfakes.OutboxRecord) errand.RejectionData {
	t.Helper()
	raw, ok := row.Payload.(json.RawMessage)
	if !ok {
		var err error
		raw, err = json.Marshal(row.Payload)
		require.NoError(t, err)
	}
	var data errand.RejectionData
	require.NoError(t, json.Unmarshal(raw, &data))
	return data
}

func TestWithdraw(t *testing.T) {
	t.Run("before the acceptance deadline the errand reopens", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(assigned(1))

		recorder := h.act(t, h.courier, errandID(1), "withdraw", 3)
		body := decode(t, recorder)
		assert.Equal(t, "OPEN", body["status"])
		assert.EqualValues(t, 4, body["version"])
		assert.Equal(t, "COURIER", body["viewerRole"])
		assert.NotContains(t, body, "contactName", "the contact person is hidden once withdrawn")
		assert.NotContains(t, body, "requester")

		e := h.stored(t, 1)
		assert.Equal(t, errand.StatusOpen, e.Status)
		assert.Empty(t, e.CourierID)
		assert.Nil(t, e.PickupDeadline)
		assert.Len(t, h.store.OutboxKinds(errand.ErrandWithdrawn.Kind()), 1)
		assert.Empty(t, h.store.OutboxKinds(errand.KindCreditRelease))

		history, err := h.store.CourierHistory(t.Context(), errandID(1), courierID, t0)
		require.NoError(t, err)
		assert.Equal(t, errand.CourierHistory{WithdrewFromErrand: true, WithdrawalsSince: 1}, history,
			"the withdrawal counts towards the restriction (F3.6.4)")

		t.Run("a retry after the withdrawal committed gets 200", func(t *testing.T) {
			retry := h.act(t, h.courier, errandID(1), "withdraw", 3)
			require.Equal(t, http.StatusOK, retry.Code, retry.Body.String())
			assert.JSONEq(t, recorder.Body.String(), retry.Body.String())
			assert.Len(t, h.store.OutboxKinds(errand.ErrandWithdrawn.Kind()), 1, "nothing new is written")
		})

		t.Run("a later withdraw by the former courier is ERRAND_UNAVAILABLE", func(t *testing.T) {
			requireError(t, h.act(t, h.courier, errandID(1), "withdraw", 4), http.StatusConflict, apperror.ErrandUnavailable)
			assert.Empty(t, h.rejections(), "only pickup is announced")
		})
	})

	t.Run("after the acceptance deadline the errand expires and the reservation is released", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(assigned(1))
		h.clock.Set(t0.Add(time.Hour))

		body := decode(t, h.act(t, h.courier, errandID(1), "withdraw", 3))
		assert.Equal(t, "EXPIRED", body["status"])
		assert.Equal(t, errand.StatusExpired, h.stored(t, 1).Status)
		assert.Len(t, h.store.OutboxKinds(errand.ErrandWithdrawn.Kind()), 1)
		assert.Len(t, h.store.OutboxKinds(errand.ErrandExpired.Kind()), 1)
		assert.Len(t, h.store.OutboxKinds(errand.KindCreditRelease), 1, "F4.3.3")
	})

	t.Run("a courier who has picked up cannot withdraw", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(pickedUp(1))
		requireError(t, h.act(t, h.courier, errandID(1), "withdraw", 4), http.StatusConflict, apperror.ErrandUnavailable)
		assert.Equal(t, errand.StatusPickedUp, h.stored(t, 1).Status)
	})
}

func TestPickUp(t *testing.T) {
	h := newHarness(t)
	h.store.Put(assigned(1))

	recorder := h.act(t, h.courier, errandID(1), "pickup", 3)
	body := decode(t, recorder)
	assert.Equal(t, "PICKED_UP", body["status"])
	assert.EqualValues(t, 4, body["version"])
	assert.Equal(t, contactName, body["contactName"])
	assert.Equal(t, contactMobile, body["contactMobile"])
	assert.Equal(t, errand.StatusPickedUp, h.stored(t, 1).Status)
	assert.Len(t, h.store.OutboxKinds(errand.ErrandPickedUp.Kind()), 1)

	t.Run("a retry after the pickup committed gets 200", func(t *testing.T) {
		retry := h.act(t, h.courier, errandID(1), "pickup", 3)
		require.Equal(t, http.StatusOK, retry.Code, retry.Body.String())
		assert.JSONEq(t, recorder.Body.String(), retry.Body.String())
		assert.Len(t, h.store.OutboxKinds(errand.ErrandPickedUp.Kind()), 1)
		assert.Empty(t, h.rejections())
	})
}

func TestPickUpRejections(t *testing.T) {
	t.Run("a courier withdrawn by the pickup deadline is told and notified once", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(assigned(1))
		late := t0.Add(31 * time.Minute)
		_, _, err := h.store.Fire(t.Context(), errandID(1), errand.EventPickupDeadlinePassed, errand.SystemActor(), errand.EventInput{}, late)
		require.NoError(t, err)
		h.clock.Set(late.Add(time.Minute))

		got := requireError(t, h.act(t, h.courier, errandID(1), "pickup", 3), http.StatusConflict, apperror.ErrandUnavailable)
		assert.Contains(t, got.Message, "no longer")
		requireError(t, h.act(t, h.courier, errandID(1), "pickup", 3), http.StatusConflict, apperror.ErrandUnavailable)

		rejections := h.rejections()
		require.Len(t, rejections, 1, "a retry of the same request notifies once")
		data := rejectionData(t, rejections[0])
		assert.Equal(t, errand.RejectedPickup, data.Action)
		assert.Equal(t, apperror.ErrandUnavailable, data.Code)
		assert.Equal(t, errand.ActorCourier, data.ActorType)
		assert.Equal(t, courierID, data.ActorID)
		assert.Equal(t, errand.StatusOpen, data.Status)
		assert.Equal(t, 4, data.ErrandVersion)
	})

	t.Run("losing the compare-and-swap to the deadline job is announced", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(assigned(1))
		h.store.FailNext("Apply", &apperror.Error{Code: apperror.ErrandUnavailable, Message: "errand is OPEN"})
		requireError(t, h.act(t, h.courier, errandID(1), "pickup", 3), http.StatusConflict, apperror.ErrandUnavailable)
		assert.Len(t, h.rejections(), 1)
	})

	t.Run("a stale version is ERRAND_CHANGED and announced", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(assigned(1))
		requireError(t, h.act(t, h.courier, errandID(1), "pickup", 2), http.StatusConflict, apperror.ErrandChanged)
		require.Len(t, h.rejections(), 1)
		assert.Equal(t, apperror.ErrandChanged, rejectionData(t, h.rejections()[0]).Code)
		assert.Equal(t, errand.StatusAssigned, h.stored(t, 1).Status)
	})

	t.Run("a failed notification still answers 409", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(assigned(1))
		h.store.FailNext("RecordRejection", errors.New("connection reset"))
		requireError(t, h.act(t, h.courier, errandID(1), "pickup", 2), http.StatusConflict, apperror.ErrandChanged)
		assert.Contains(t, h.logs.String(), "recording a pickup rejection failed")
	})
}

func TestDeliver(t *testing.T) {
	h := newHarness(t)
	h.store.Put(pickedUp(1))
	settings := errand.DefaultSystemSettings()
	settings.Deadlines.VerificationAfter = 45 * time.Minute
	h.settings.Set(settings)

	recorder := h.act(t, h.courier, errandID(1), "deliver", 4)
	body := decode(t, recorder)
	assert.Equal(t, "DELIVERED", body["status"])
	assert.EqualValues(t, 5, body["version"])

	e := h.stored(t, 1)
	assert.Equal(t, errand.StatusDelivered, e.Status)
	require.NotNil(t, e.DeliveredAt)
	assert.Equal(t, t0, *e.DeliveredAt)
	require.NotNil(t, e.VerificationDeadline)
	assert.Equal(t, t0.Add(45*time.Minute), *e.VerificationDeadline, "the deadline comes from settings")
	assert.Len(t, h.store.OutboxKinds(errand.ErrandDelivered.Kind()), 1)
	assert.Empty(t, h.store.OutboxKinds(errand.KindCreditTransfer), "credits move when the requester confirms")

	t.Run("a retry after the delivery committed gets 200", func(t *testing.T) {
		h.settings.FailNext("Get", errors.New("must not be read"))
		retry := h.act(t, h.courier, errandID(1), "deliver", 4)
		require.Equal(t, http.StatusOK, retry.Code, retry.Body.String())
		assert.JSONEq(t, recorder.Body.String(), retry.Body.String())
		assert.Len(t, h.store.OutboxKinds(errand.ErrandDelivered.Kind()), 1)
	})

	t.Run("delivering before pickup is INVALID_TRANSITION", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(assigned(1))
		requireError(t, h.act(t, h.courier, errandID(1), "deliver", 3), http.StatusConflict, apperror.InvalidTransition)
		assert.Equal(t, errand.StatusAssigned, h.stored(t, 1).Status)
	})

	t.Run("settings unreadable refuses the delivery", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(pickedUp(1))
		h.settings.FailNext("Get", errors.New("settings table unreadable"))
		requireError(t, h.act(t, h.courier, errandID(1), "deliver", 4), http.StatusInternalServerError, apperror.Internal)
		assert.Equal(t, errand.StatusPickedUp, h.stored(t, 1).Status)
	})
}

func TestReportIssue(t *testing.T) {
	h := newHarness(t)
	h.store.Put(pickedUp(1))

	recorder := h.report(t, h.courier, errandID(1), 4, "Shop closed, items unavailable")
	body := decode(t, recorder)
	assert.Equal(t, "REVIEW_REQUIRED", body["status"])
	assert.Equal(t, "COURIER_ISSUE", body["reviewReason"])

	e := h.stored(t, 1)
	assert.Equal(t, errand.StatusReviewRequired, e.Status)
	assert.Equal(t, errand.ReviewCourierIssue, e.ReviewReason)
	reviews := h.store.Reviews()
	require.Len(t, reviews, 1)
	assert.Equal(t, errand.ReviewCourierIssue, reviews[0].Reason)
	assert.Equal(t, courierID, reviews[0].ReporterID)
	assert.Equal(t, "Shop closed, items unavailable", reviews[0].Note)
	assert.Len(t, h.store.OutboxKinds(errand.ErrandReviewRequired.Kind()), 1)

	t.Run("a retry after the report committed gets 200", func(t *testing.T) {
		retry := h.report(t, h.courier, errandID(1), 4, "Shop closed, items unavailable")
		require.Equal(t, http.StatusOK, retry.Code, retry.Body.String())
		assert.JSONEq(t, recorder.Body.String(), retry.Body.String())
		assert.Len(t, h.store.Reviews(), 1)
	})

	t.Run("only after pickup", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(assigned(1))
		requireError(t, h.report(t, h.courier, errandID(1), 3, "cannot find the shop"), http.StatusConflict, apperror.InvalidTransition)
		assert.Empty(t, h.store.Reviews())
	})

	t.Run("the reason is required", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(pickedUp(1))
		got := requireError(t, h.report(t, h.courier, errandID(1), 4, "   "), http.StatusBadRequest, apperror.ValidationFailed)
		require.Len(t, got.Fields, 1)
		assert.Equal(t, "reason", got.Fields[0].Field)
		got = requireError(t, h.act(t, h.courier, errandID(1), "report-issue", 4), http.StatusBadRequest, apperror.ValidationFailed)
		assert.Equal(t, []apperror.Field{{Field: "reason", Message: "is required"}}, got.Fields)
		assert.Equal(t, errand.StatusPickedUp, h.stored(t, 1).Status)
	})
}

func TestOnlyTheAssignedCourier(t *testing.T) {
	actions := []string{"withdraw", "pickup", "deliver", "report-issue"}
	for _, action := range actions {
		t.Run(action, func(t *testing.T) {
			h := newHarness(t)
			h.store.Put(assigned(1))
			body := `{"expectedVersion":3,"reason":"x"}`
			if action != "report-issue" {
				body = `{"expectedVersion":3}`
			}
			for _, token := range []string{h.rival, h.requester} {
				requireError(t, h.post(t, token, errandID(1), action, body), http.StatusForbidden, apperror.Forbidden)
			}
			assert.Equal(t, errand.StatusAssigned, h.stored(t, 1).Status)
			assert.Empty(t, h.rejections(), "403s are not announced")
		})
	}

	t.Run("a stranger is refused even when the errand has moved on", func(t *testing.T) {
		h := newHarness(t)
		e := assigned(1)
		e.Status, e.Version, e.CourierID = errand.StatusOpen, 4, ""
		h.store.Put(e)
		requireError(t, h.act(t, h.rival, errandID(1), "pickup", 3), http.StatusForbidden, apperror.Forbidden)
		assert.Empty(t, h.rejections())
	})

	t.Run("a former courier cannot see the new courier's errand", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(assigned(1))
		require.Equal(t, http.StatusOK, h.act(t, h.courier, errandID(1), "withdraw", 3).Code)
		e := h.stored(t, 1)
		e.Status, e.Version, e.CourierID = errand.StatusPickedUp, 6, rivalID
		h.store.Put(e)

		requireError(t, h.act(t, h.courier, errandID(1), "pickup", 5), http.StatusConflict, apperror.ErrandUnavailable)
		requireError(t, h.act(t, h.courier, errandID(1), "deliver", 6), http.StatusConflict, apperror.ErrandUnavailable)
		assert.Equal(t, errand.StatusPickedUp, h.stored(t, 1).Status)
	})

	t.Run("history unreadable is INTERNAL", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(assigned(1))
		h.store.FailNext("CourierHistory", errors.New("connection reset"))
		requireError(t, h.act(t, h.rival, errandID(1), "pickup", 3), http.StatusInternalServerError, apperror.Internal)
	})
}

func TestInvalidRequest(t *testing.T) {
	cases := []struct {
		name   string
		id     string
		body   string
		status int
		code   apperror.Code
		fields []apperror.Field
	}{
		{"malformed errand ID", "not-a-uuid", `{"expectedVersion":3}`, http.StatusBadRequest, apperror.InvalidArgument, nil},
		{"unknown errand", errandID(9), `{"expectedVersion":3}`, http.StatusNotFound, apperror.ErrandNotFound, nil},
		{"empty body", errandID(1), ``, http.StatusBadRequest, apperror.InvalidArgument, nil},
		{"missing version", errandID(1), `{}`, http.StatusBadRequest, apperror.ValidationFailed,
			[]apperror.Field{{Field: "expectedVersion", Message: "is required"}}},
		{"identity in the body", errandID(1), `{"expectedVersion":3,"courierId":"auth0|someone"}`, http.StatusBadRequest,
			apperror.ValidationFailed, []apperror.Field{{Field: "courierId", Message: "is not allowed"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.store.Put(assigned(1))
			got := requireError(t, h.post(t, h.courier, tc.id, "pickup", tc.body), tc.status, tc.code)
			assert.Equal(t, tc.fields, got.Fields)
			assert.Equal(t, errand.StatusAssigned, h.stored(t, 1).Status)
			assert.Empty(t, h.rejections())
		})
	}
}

func TestSupplierServiceDownWritesNothing(t *testing.T) {
	h := newHarness(t)
	h.store.Put(assigned(1))
	h.suppliers.FailNext("GetVersion", &apperror.Error{Code: apperror.DependencyUnavailable, Message: "down"})
	requireError(t, h.act(t, h.courier, errandID(1), "pickup", 3), http.StatusServiceUnavailable, apperror.DependencyUnavailable)
	assert.Equal(t, errand.StatusAssigned, h.stored(t, 1).Status, "read before the write")
	assert.Equal(t, http.StatusOK, h.act(t, h.courier, errandID(1), "pickup", 3).Code)
}

func TestAuthorization(t *testing.T) {
	h := newHarness(t)
	h.store.Put(assigned(1))
	requireError(t, h.act(t, "", errandID(1), "pickup", 3), http.StatusUnauthorized, apperror.Unauthenticated)
	requireError(t, h.act(t, tokenFor(t, h.fixture, courierID, auth.ReviewErrands), errandID(1), "pickup", 3),
		http.StatusForbidden, apperror.Forbidden)
	service := h.fixture.Token(t, h.fixture.ServiceClaims(auth.OrderAudience, []string{string(auth.UseErrands)}), "one")
	requireError(t, h.act(t, service, errandID(1), "pickup", 3), http.StatusForbidden, apperror.Forbidden)
	assert.Equal(t, errand.StatusAssigned, h.stored(t, 1).Status)
	assert.Empty(t, h.rejections())
}
