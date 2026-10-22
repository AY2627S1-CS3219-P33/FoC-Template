package mine_test

import (
	"encoding/base64"
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
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand/mine"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/httpapi"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/testfakes"
)

const (
	requesterID = "auth0|requester-1"
	courierID   = "auth0|courier-1"
	otherID     = "auth0|other-1"

	supplierID    = "0b8e6a52-3c1d-4f7e-8a9b-1c2d3e4f5a6b"
	oldVersionID  = "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d"
	versionID     = "5d2c1b0a-9f8e-4d7c-8b6a-5f4e3d2c1b0a"
	buildingID    = "7a6b5c4d-3e2f-4a1b-9c8d-7e6f5a4b3c2d"
	contactName   = "Alex Tan"
	contactMobile = "+65 9123 4567"

	requestedPath  = "/me/errands/requested"
	deliveringPath = "/me/errands/delivering"
	historyPath    = "/me/errands/history"
)

var t0 = time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)

func errandID(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) }

func ptr[T any](v T) *T { return &v }

// errandOf is errand n in status, requested by requesterID and delivered by
// courier (empty for none). It was created n minutes after t0 and last
// updated 2n minutes after t0, so higher n sorts first on both lists' keys.
func errandOf(n int, status errand.Status, courier string) errand.Errand {
	e := errand.Errand{
		ID:          errandID(n),
		Status:      status,
		Version:     3,
		RequesterID: requesterID,
		CourierID:   courier,
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
		CreatedAt: t0.Add(time.Duration(n) * time.Minute),
		UpdatedAt: t0.Add(time.Duration(2*n) * time.Minute),
	}
	if courier != "" {
		e.PickupDeadline = ptr(t0.Add(90 * time.Minute))
		e.DeliveryDeadline = ptr(t0.Add(150 * time.Minute))
	}
	return e
}

type harness struct {
	router    http.Handler
	store     *testfakes.FakeStore
	suppliers *testfakes.FakeSupplierGate
	logs      *strings.Builder
	fixture   *authtest.Fixture
	requester string
	courier   string
	other     string
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
	// The old version stays readable after the supplier moves on (F2.5.1).
	suppliers.AddSupplier(errand.SupplierVersion{
		SupplierID: supplierID, VersionID: oldVersionID, Name: "Kopi Corner (old)",
		Building: "COM1", Floor: "1", LocationDescription: "by the stairs",
		Latitude: 1.2949, Longitude: 103.7737,
	})
	suppliers.AddSupplier(errand.SupplierVersion{
		SupplierID: supplierID, VersionID: versionID, Name: "Kopi Corner",
		Building: "COM1", Floor: "1", LocationDescription: "next to the lift",
		Latitude: 1.2949, Longitude: 103.7737,
	})
	handler := mine.NewHandler(mine.Dependencies{Store: store, Suppliers: suppliers, Logger: logger}, auth.NewMiddleware(verifier))
	return harness{
		router: httpapi.NewRouter(httpapi.Dependencies{Logger: logger}, handler),
		store:  store, suppliers: suppliers, logs: logs, fixture: f,
		requester: tokenFor(t, f, requesterID, auth.UseErrands),
		courier:   tokenFor(t, f, courierID, auth.UseErrands),
		other:     tokenFor(t, f, otherID, auth.UseErrands),
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

func (h harness) get(t *testing.T, token, target string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	h.router.ServeHTTP(recorder, request)
	return recorder
}

type pageBody struct {
	Items      []map[string]any `json:"items"`
	NextCursor string           `json:"nextCursor"`
}

func decodePage(t *testing.T, recorder *httptest.ResponseRecorder) pageBody {
	t.Helper()
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var body pageBody
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	require.NotNil(t, body.Items, "items is [] when empty, never null")
	return body
}

func ids(p pageBody) []string {
	out := make([]string, 0, len(p.Items))
	for _, item := range p.Items {
		out = append(out, item["errandId"].(string))
	}
	return out
}

func requireError(t *testing.T, recorder *httptest.ResponseRecorder, status int, code apperror.Code) {
	t.Helper()
	require.Equal(t, status, recorder.Code, recorder.Body.String())
	var body apperror.Error
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	require.Equal(t, code, body.Code, recorder.Body.String())
}

// assertNoCourier checks that a requester item says nothing about the
// courier (NFR3.6.4).
func assertNoCourier(t *testing.T, item map[string]any) {
	t.Helper()
	for _, key := range []string{"courierId", "courier", "courierName", "courierMobile"} {
		assert.NotContains(t, item, key)
	}
}

func assertNoRequester(t *testing.T, item map[string]any) {
	t.Helper()
	for _, key := range []string{"requesterId", "requester"} {
		assert.NotContains(t, item, key)
	}
}

func TestRequested(t *testing.T) {
	h := newHarness(t)
	open := errandOf(1, errand.StatusOpen, "")
	open.PendingEdit = &errand.PendingEdit{
		Details: open.Details,
		Key:     errandID(1) + ":edit:3",
		At:      t0.Add(5 * time.Minute),
	}
	open.PendingEdit.Details.SupplierVersionID = oldVersionID
	open.PendingEdit.Details.Reward = 15
	h.store.Put(open)
	h.store.Put(errandOf(2, errand.StatusAssigned, courierID))
	h.store.Put(errandOf(3, errand.StatusPendingReservation, ""))
	h.store.Put(errandOf(4, errand.StatusCompleted, courierID))
	someoneElses := errandOf(5, errand.StatusOpen, "")
	someoneElses.RequesterID = otherID
	h.store.Put(someoneElses)

	p := decodePage(t, h.get(t, h.requester, requestedPath))
	assert.Equal(t, []string{errandID(3), errandID(2), errandID(1)}, ids(p),
		"active errands only, newest created first (F3.11)")
	assert.Empty(t, p.NextCursor)

	for _, item := range p.Items {
		assert.Equal(t, "REQUESTER", item["viewerRole"])
		assert.Equal(t, contactName, item["contactName"])
		assert.Equal(t, contactMobile, item["contactMobile"])
		assert.Equal(t, "Kopi Corner", item["pickup"].(map[string]any)["name"])
		assertNoCourier(t, item)
	}
	assigned := p.Items[1]
	assert.Equal(t, "ASSIGNED", assigned["status"], "the requester sees the status of an assigned errand")

	edit := p.Items[2]["pendingEdit"].(map[string]any)
	assert.EqualValues(t, 15, edit["reward"])
	assert.Equal(t, "Kopi Corner (old)", edit["pickup"].(map[string]any)["name"],
		"the pending edit's pickup comes from its own version")

	assert.Len(t, h.suppliers.CallsTo("GetVersion"), 2, "each supplier version is read once per page")

	t.Run("a courier has requested nothing", func(t *testing.T) {
		assert.Empty(t, decodePage(t, h.get(t, h.courier, requestedPath)).Items)
	})
}

func TestDelivering(t *testing.T) {
	h := newHarness(t)
	h.store.Put(errandOf(1, errand.StatusAssigned, courierID))
	h.store.Put(errandOf(2, errand.StatusPickedUp, courierID))
	h.store.Put(errandOf(3, errand.StatusDelivered, courierID))
	review := errandOf(4, errand.StatusReviewRequired, courierID)
	review.ReviewReason = errand.ReviewCourierIssue
	h.store.Put(review)
	h.store.Put(errandOf(5, errand.StatusCompleted, courierID))
	h.store.Put(errandOf(6, errand.StatusAssigned, otherID))
	ownRequest := errandOf(7, errand.StatusOpen, "")
	ownRequest.RequesterID = courierID
	h.store.Put(ownRequest)

	p := decodePage(t, h.get(t, h.courier, deliveringPath))
	assert.Equal(t, []string{errandID(4), errandID(3), errandID(2), errandID(1)}, ids(p),
		"active deliveries only, most recently updated first (F3.12)")
	for _, item := range p.Items {
		assert.Equal(t, "COURIER", item["viewerRole"])
		assert.Equal(t, "less ice", item["notes"], "the courier sees the requester's notes (F3.12)")
		assert.Equal(t, contactName, item["contactName"], "the contact person is shown while the errand is active")
		assert.Equal(t, contactMobile, item["contactMobile"])
		assertNoRequester(t, item)
	}
	assert.Equal(t, "COURIER_ISSUE", p.Items[0]["reviewReason"])

	t.Run("the requester delivers nothing", func(t *testing.T) {
		assert.Empty(t, decodePage(t, h.get(t, h.requester, deliveringPath)).Items)
	})
}

func TestHistory(t *testing.T) {
	h := newHarness(t)
	h.store.Put(errandOf(1, errand.StatusCompleted, courierID))
	h.store.Put(errandOf(2, errand.StatusCancelled, ""))
	h.store.Put(errandOf(3, errand.StatusRefunded, courierID))
	// The courier withdrew past the acceptance deadline: no longer theirs.
	h.store.Put(errandOf(4, errand.StatusExpired, ""))
	h.store.Put(errandOf(5, errand.StatusRejected, ""))
	h.store.Put(errandOf(6, errand.StatusPickedUp, courierID))
	// The supplier was deleted afterwards; its versions stay readable (F2.4.3).
	h.suppliers.SetUnavailable(supplierID)

	t.Run("the requester sees every finished errand in the requester view", func(t *testing.T) {
		p := decodePage(t, h.get(t, h.requester, historyPath))
		assert.Equal(t, []string{errandID(5), errandID(4), errandID(3), errandID(2), errandID(1)}, ids(p),
			"terminal errands only, most recently updated first (F3.13)")
		for _, item := range p.Items {
			assert.Equal(t, "REQUESTER", item["viewerRole"])
			assert.Equal(t, contactName, item["contactName"])
			assert.Equal(t, "Kopi Corner", item["pickup"].(map[string]any)["name"])
			assertNoCourier(t, item)
		}
	})

	t.Run("the courier sees what they delivered, without the contact person", func(t *testing.T) {
		p := decodePage(t, h.get(t, h.courier, historyPath))
		assert.Equal(t, []string{errandID(3), errandID(1)}, ids(p),
			"an errand the courier withdrew from is not listed")
		for _, item := range p.Items {
			assert.Equal(t, "COURIER", item["viewerRole"])
			assert.NotContains(t, item, "contactName", "NFR3.6.4: terminal errands hide the contact person")
			assert.NotContains(t, item, "contactMobile")
			assert.Equal(t, "Kopi Corner", item["pickup"].(map[string]any)["name"])
			assertNoRequester(t, item)
		}
	})

	t.Run("one caller can hold both roles across errands", func(t *testing.T) {
		delivered := errandOf(7, errand.StatusCompleted, requesterID)
		delivered.RequesterID = otherID
		h.store.Put(delivered)

		p := decodePage(t, h.get(t, h.requester, historyPath))
		require.Equal(t, errandID(7), p.Items[0]["errandId"])
		assert.Equal(t, "COURIER", p.Items[0]["viewerRole"])
		assert.NotContains(t, p.Items[0], "contactName")
		assert.Equal(t, "REQUESTER", p.Items[1]["viewerRole"])
	})
}

func TestPagination(t *testing.T) {
	h := newHarness(t)
	for n := 1; n <= 5; n++ {
		h.store.Put(errandOf(n, errand.StatusOpen, ""))
		h.store.Put(errandOf(10+n, errand.StatusCompleted, ""))
	}

	for _, tc := range []struct {
		path string
		want []string
	}{
		{requestedPath, []string{errandID(5), errandID(4), errandID(3), errandID(2), errandID(1)}},
		{historyPath, []string{errandID(15), errandID(14), errandID(13), errandID(12), errandID(11)}},
	} {
		t.Run(tc.path, func(t *testing.T) {
			var got []string
			target := tc.path + "?limit=2"
			for pages := 1; ; pages++ {
				require.LessOrEqual(t, pages, 3)
				p := decodePage(t, h.get(t, h.requester, target))
				got = append(got, ids(p)...)
				if p.NextCursor == "" {
					assert.Equal(t, 3, pages)
					break
				}
				target = tc.path + "?limit=2&cursor=" + p.NextCursor
			}
			assert.Equal(t, tc.want, got)
		})
	}

	t.Run("the default page size is 25", func(t *testing.T) {
		h := newHarness(t)
		for n := 1; n <= 30; n++ {
			h.store.Put(errandOf(n, errand.StatusOpen, ""))
		}
		p := decodePage(t, h.get(t, h.requester, requestedPath))
		assert.Len(t, p.Items, mine.DefaultPageSize)
		assert.NotEmpty(t, p.NextCursor)
		assert.Len(t, decodePage(t, h.get(t, h.requester, requestedPath+"?limit=100")).Items, 30)
	})

	t.Run("a cursor is bound to its list", func(t *testing.T) {
		p := decodePage(t, h.get(t, h.requester, requestedPath+"?limit=1"))
		require.NotEmpty(t, p.NextCursor)
		requireError(t, h.get(t, h.requester, historyPath+"?cursor="+p.NextCursor),
			http.StatusBadRequest, apperror.InvalidArgument)
	})

	t.Run("a cursor passed to another user pages that user's own errands", func(t *testing.T) {
		p := decodePage(t, h.get(t, h.requester, requestedPath+"?limit=1"))
		other := decodePage(t, h.get(t, h.other, requestedPath+"?cursor="+p.NextCursor))
		assert.Empty(t, other.Items)
	})
}

func TestInvalidQuery(t *testing.T) {
	h := newHarness(t)
	h.store.Put(errandOf(1, errand.StatusOpen, ""))
	encode := func(v string) string { return base64.RawURLEncoding.EncodeToString([]byte(v)) }

	for name, query := range map[string]string{
		"limit 0":            "limit=0",
		"limit above 100":    "limit=101",
		"limit not a number": "limit=ten",
		"empty cursor":       "cursor=",
		"cursor not base64":  "cursor=***",
		"cursor not JSON":    "cursor=" + encode("not json"),
		"cursor without id":  "cursor=" + encode(`{"l":"requested","t":"2026-03-02T09:00:00Z"}`),
		"cursor id not UUID": "cursor=" + encode(`{"l":"requested","t":"2026-03-02T09:00:00Z","i":"x"}`),
		"cursor without at":  "cursor=" + encode(`{"l":"requested","i":"`+errandID(1)+`"}`),
		"cursor too long":    "cursor=" + strings.Repeat("A", 513),
	} {
		t.Run(name, func(t *testing.T) {
			requireError(t, h.get(t, h.requester, requestedPath+"?"+query), http.StatusBadRequest, apperror.InvalidArgument)
		})
	}

	t.Run("a well-formed cursor is accepted", func(t *testing.T) {
		cursor := encode(`{"l":"requested","t":"2026-03-02T10:00:00Z","i":"` + strings.ToUpper(errandID(9)) + `"}`)
		assert.Equal(t, []string{errandID(1)}, ids(decodePage(t, h.get(t, h.requester, requestedPath+"?cursor="+cursor))))
	})
}

func TestEmptyList(t *testing.T) {
	h := newHarness(t)
	for _, path := range []string{requestedPath, deliveringPath, historyPath} {
		recorder := h.get(t, h.other, path)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.JSONEq(t, `{"items":[]}`, recorder.Body.String())
	}
	assert.Empty(t, h.suppliers.CallsTo("GetVersion"))
}

func TestFailures(t *testing.T) {
	t.Run("supplier-service down is 503", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(errandOf(1, errand.StatusOpen, ""))
		h.suppliers.FailNext("GetVersion", &apperror.Error{Code: apperror.DependencyUnavailable, Message: "down"})
		requireError(t, h.get(t, h.requester, requestedPath), http.StatusServiceUnavailable, apperror.DependencyUnavailable)
	})

	t.Run("an unknown supplier version is 500, not 422", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(errandOf(1, errand.StatusOpen, ""))
		h.suppliers.FailNext("GetVersion", &apperror.Error{Code: apperror.SupplierUnavailable, Message: "no such version"})
		requireError(t, h.get(t, h.requester, requestedPath), http.StatusInternalServerError, apperror.Internal)
	})

	for method, path := range map[string]string{
		"ListRequested":  requestedPath,
		"ListDelivering": deliveringPath,
		"ListHistory":    historyPath,
	} {
		t.Run("a store failure in "+method+" is 500 and logs no request data", func(t *testing.T) {
			h := newHarness(t)
			h.store.Put(errandOf(1, errand.StatusOpen, ""))
			h.store.FailNext(method, errors.New("connection reset"))
			requireError(t, h.get(t, h.requester, path), http.StatusInternalServerError, apperror.Internal)
			assert.Contains(t, h.logs.String(), "connection reset")
			assert.NotContains(t, h.logs.String(), contactMobile)
			assert.NotContains(t, h.logs.String(), requesterID)
		})
	}
}

func TestAuthorization(t *testing.T) {
	h := newHarness(t)
	h.store.Put(errandOf(1, errand.StatusOpen, ""))
	admin := tokenFor(t, h.fixture, requesterID, auth.ReviewErrands)
	service := h.fixture.Token(t, h.fixture.ServiceClaims(auth.OrderAudience, []string{string(auth.UseErrands)}), "one")
	for _, path := range []string{requestedPath, deliveringPath, historyPath} {
		requireError(t, h.get(t, "", path), http.StatusUnauthorized, apperror.Unauthenticated)
		requireError(t, h.get(t, admin, path), http.StatusForbidden, apperror.Forbidden)
		requireError(t, h.get(t, service, path), http.StatusForbidden, apperror.Forbidden)
	}
}
