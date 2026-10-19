package browse_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/cache"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand/browse"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/httpapi"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/testfakes"
)

const (
	requesterID = "auth0|requester-1"
	courierID   = "auth0|courier-1"
	otherID     = "auth0|courier-2"

	kopiID        = "0b8e6a52-3c1d-4f7e-8a9b-1c2d3e4f5a6b"
	kopiVersionID = "5d2c1b0a-9f8e-4d7c-8b6a-5f4e3d2c1b0a"
	utownID       = "1c9f7b63-4d2e-4a8f-9bac-2d3e4f5a6b7c"
	utownVersion  = "6e3d2c1b-0a9f-4e8d-9c7b-6a5f4e3d2c1b"
	buildingID    = "7a6b5c4d-3e2f-4a1b-9c8d-7e6f5a4b3c2d"
	contactName   = "Alex Tan"
	contactMobile = "+65 9123 4567"
)

var t0 = time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)

func errandID(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) }

func ptr[T any](v T) *T { return &v }

// openErrand is OPEN at version 2 from Kopi Corner, created 20 minutes
// before t0, with its acceptance deadline deadlineIn after t0.
func openErrand(n int, deadlineIn time.Duration) errand.Errand {
	return errand.Errand{
		ID:          errandID(n),
		Status:      errand.StatusOpen,
		Version:     2,
		RequesterID: requesterID,
		Details: errand.Details{
			SupplierID:               kopiID,
			SupplierVersionID:        kopiVersionID,
			DeliveryBuildingID:       buildingID,
			DeliveryBuildingSnapshot: json.RawMessage(`{"id":"` + buildingID + `","name":"COM1","retired":false}`),
			DeliveryDetail:           "Level 2 lounge",
			Items:                    json.RawMessage(`[{"description":"iced latte","quantity":2}]`),
			Notes:                    "less ice",
			ContactName:              contactName,
			ContactMobile:            contactMobile,
			Reward:                   10,
			AcceptanceDeadline:       t0.Add(deadlineIn),
		},
		CreatedAt: t0.Add(-20 * time.Minute),
		UpdatedAt: t0.Add(-19 * time.Minute),
	}
}

func fromUTown(e errand.Errand) errand.Errand {
	e.Details.SupplierID, e.Details.SupplierVersionID = utownID, utownVersion
	return e
}

func assigned(e errand.Errand, status errand.Status) errand.Errand {
	e.Status, e.Version, e.CourierID = status, 3, courierID
	e.PickupDeadline, e.DeliveryDeadline = ptr(t0.Add(time.Hour)), ptr(t0.Add(2*time.Hour))
	return e
}

// countingStore counts ListOpen calls and, while gate is set, blocks them
// until it is closed.
type countingStore struct {
	*testfakes.FakeStore
	listOpen atomic.Int64
	gate     chan struct{}
}

func (s *countingStore) ListOpen(ctx context.Context, q errand.OpenQuery) (errand.Page[errand.OpenCursor], error) {
	s.listOpen.Add(1)
	if s.gate != nil {
		<-s.gate
	}
	return s.FakeStore.ListOpen(ctx, q)
}

type harness struct {
	router    http.Handler
	store     *countingStore
	suppliers *testfakes.FakeSupplierGate
	cache     *testfakes.FakeOpenErrandCache
	clock     *testfakes.FakeClock
	logs      *strings.Builder
	fixture   *authtest.Fixture
	requester string
	courier   string
	other     string
}

// newHarness uses the fake cache; newHarnessWith picks the cache.
func newHarness(t *testing.T) harness { return newHarnessWith(t, nil) }

func newHarnessWith(t *testing.T, openCache errand.OpenErrandCache) harness {
	t.Helper()
	f := authtest.New(t)
	cfg := auth.DefaultAuth0Config()
	cfg.Issuer = f.Server.URL + "/"
	verifier, err := auth.NewAuth0(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), auth.WithHTTPClient(f.Server.Client()))
	require.NoError(t, err)

	logs := &strings.Builder{}
	logger := slog.New(slog.NewTextHandler(&lockedWriter{w: logs}, nil))
	clock := testfakes.NewFakeClock(t0)
	store := &countingStore{FakeStore: testfakes.NewFakeStore()}
	suppliers := testfakes.NewFakeSupplierGate()
	suppliers.AddSupplier(errand.SupplierVersion{
		SupplierID: kopiID, VersionID: kopiVersionID, Name: "Kopi Corner",
		Building: "COM1", Floor: "1", LocationDescription: "next to the lift",
		Latitude: 1.2949, Longitude: 103.7737,
	})
	suppliers.AddSupplier(errand.SupplierVersion{
		SupplierID: utownID, VersionID: utownVersion, Name: "UTown Deli",
		Building: "Stephen Riady Centre", Floor: "1", LocationDescription: "food court",
		Latitude: 1.3049, Longitude: 103.7727,
	})
	fakeCache := testfakes.NewFakeOpenErrandCache(clock)
	if openCache == nil {
		openCache = fakeCache
	}
	handler := browse.NewHandler(browse.Dependencies{
		Store: store, Suppliers: suppliers, Cache: openCache, Clock: clock, Logger: logger,
	}, auth.NewMiddleware(verifier))
	return harness{
		router: httpapi.NewRouter(httpapi.Dependencies{Logger: logger}, handler),
		store:  store, suppliers: suppliers, cache: fakeCache, clock: clock, logs: logs, fixture: f,
		requester: tokenFor(t, f, requesterID, auth.UseErrands),
		courier:   tokenFor(t, f, courierID, auth.UseErrands),
		other:     tokenFor(t, f, otherID, auth.UseErrands),
	}
}

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
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

func (h harness) get(t *testing.T, token, path string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	h.router.ServeHTTP(recorder, request)
	return recorder
}

type page struct {
	Items      []map[string]any `json:"items"`
	NextCursor string           `json:"nextCursor"`
}

func (h harness) list(t *testing.T, query url.Values) page {
	t.Helper()
	recorder := h.get(t, h.other, "/errands/open?"+query.Encode())
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var p page
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &p))
	return p
}

func ids(p page) []string {
	out := []string{}
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

// ---------------------------------------------------------------------------
// GET /errands/open
// ---------------------------------------------------------------------------

func TestListOpen(t *testing.T) {
	caches := map[string]func(*testing.T) harness{
		"fake cache":  newHarness,
		"no-op cache": func(t *testing.T) harness { return newHarnessWith(t, cache.NoopOpenErrandCache{}) },
	}
	for name, newH := range caches {
		t.Run(name, func(t *testing.T) {
			h := newH(t)
			h.store.Put(openErrand(3, 2*time.Hour))
			h.store.Put(openErrand(1, time.Hour))
			h.store.Put(openErrand(2, time.Hour)) // same deadline as 1: ordered by ID
			h.store.Put(openErrand(4, 0))         // deadline is now: hidden
			pending := openErrand(5, time.Hour)
			pending.Status = errand.StatusPendingReservation
			h.store.Put(pending)
			h.store.Put(assigned(openErrand(6, time.Hour), errand.StatusAssigned))

			p := h.list(t, nil)
			assert.Equal(t, []string{errandID(1), errandID(2), errandID(3)}, ids(p))
			assert.Empty(t, p.NextCursor)

			item, err := json.Marshal(p.Items[0])
			require.NoError(t, err)
			assert.JSONEq(t, `{
				"errandId": "`+errandID(1)+`",
				"version": 2,
				"supplierId": "`+kopiID+`",
				"supplierVersionId": "`+kopiVersionID+`",
				"pickup": {"name": "Kopi Corner", "building": "COM1", "floor": "1",
					"locationDescription": "next to the lift", "latitude": 1.2949, "longitude": 103.7737},
				"deliveryBuilding": {"id": "`+buildingID+`", "name": "COM1"},
				"deliveryDetail": "Level 2 lounge",
				"items": [{"description": "iced latte", "quantity": 2}],
				"reward": 10,
				"createdAt": "2026-03-02T08:40:00Z",
				"acceptanceDeadline": "2026-03-02T10:00:00Z",
				"groupKey": "`+kopiID+`:2026-03-02T08:30Z"
			}`, string(item), "no requester, notes, contact or distance")
		})
	}
}

func TestListOpenPagination(t *testing.T) {
	h := newHarness(t)
	for n := 1; n <= 5; n++ {
		h.store.Put(openErrand(n, time.Duration(n)*time.Hour))
	}
	var got []string
	query := url.Values{"limit": {"2"}}
	for pages := 0; ; pages++ {
		require.Less(t, pages, 5, "pagination does not end")
		p := h.list(t, query)
		assert.LessOrEqual(t, len(p.Items), 2)
		got = append(got, ids(p)...)
		if p.NextCursor == "" {
			break
		}
		query.Set("cursor", p.NextCursor)
	}
	assert.Equal(t, []string{errandID(1), errandID(2), errandID(3), errandID(4), errandID(5)}, got)

	t.Run("an errand that leaves between pages does not shift the next page", func(t *testing.T) {
		p1 := h.list(t, url.Values{"limit": {"2"}})
		h.store.Put(assigned(openErrand(1, time.Hour), errand.StatusAssigned))
		h.clock.Advance(browse.OpenSetTTL) // the cache expires
		p2 := h.list(t, url.Values{"limit": {"2"}, "cursor": {p1.NextCursor}})
		assert.Equal(t, []string{errandID(3), errandID(4)}, ids(p2))
	})
}

func TestListOpenSupplierFilter(t *testing.T) {
	h := newHarness(t)
	h.store.Put(openErrand(1, time.Hour))
	h.store.Put(fromUTown(openErrand(2, time.Hour)))
	h.store.Put(fromUTown(openErrand(3, 2*time.Hour)))

	p := h.list(t, url.Values{"supplierId": {strings.ToUpper(utownID)}, "limit": {"1"}})
	assert.Equal(t, []string{errandID(2)}, ids(p))
	p = h.list(t, url.Values{"supplierId": {utownID}, "limit": {"1"}, "cursor": {p.NextCursor}})
	assert.Equal(t, []string{errandID(3)}, ids(p))
	assert.Empty(t, p.NextCursor)
}

func TestListOpenByDistance(t *testing.T) {
	h := newHarness(t)
	h.store.Put(openErrand(1, time.Hour))              // Kopi, ~1.1 km away
	h.store.Put(fromUTown(openErrand(2, 3*time.Hour))) // UTown, here
	h.store.Put(fromUTown(openErrand(3, 2*time.Hour))) // UTown, earlier deadline

	at := url.Values{"lat": {"1.3049"}, "lng": {"103.7727"}}
	p := h.list(t, at)
	assert.Equal(t, []string{errandID(3), errandID(2), errandID(1)}, ids(p), "distance, then deadline")
	assert.InDelta(t, 0, p.Items[0]["distanceMeters"], 0.001)
	assert.InDelta(t, 1117.5, p.Items[2]["distanceMeters"], 2)

	paged := url.Values{"lat": at["lat"], "lng": at["lng"], "limit": {"2"}}
	first := h.list(t, paged)
	paged.Set("cursor", first.NextCursor)
	assert.Equal(t, []string{errandID(1)}, ids(h.list(t, paged)))

	assert.NotContains(t, h.logs.String(), "103.7727", "coordinates are never logged")
	for _, call := range h.cache.CallsTo("Set") {
		assert.NotContains(t, fmt.Sprint(call.Args), "103.7727")
	}
}

func TestListOpenCursorIsBoundToFilters(t *testing.T) {
	h := newHarness(t)
	for n := 1; n <= 3; n++ {
		h.store.Put(openErrand(n, time.Duration(n)*time.Hour))
	}
	next := h.list(t, url.Values{"limit": {"1"}}).NextCursor
	require.NotEmpty(t, next)

	for name, query := range map[string]url.Values{
		"another supplier": {"cursor": {next}, "supplierId": {kopiID}},
		"coordinates":      {"cursor": {next}, "lat": {"1.3"}, "lng": {"103.7"}},
	} {
		t.Run(name, func(t *testing.T) {
			requireError(t, h.get(t, h.other, "/errands/open?"+query.Encode()), http.StatusBadRequest, apperror.InvalidArgument)
		})
	}
	// The limit is not a filter.
	assert.Len(t, h.list(t, url.Values{"cursor": {next}, "limit": {"5"}}).Items, 2)
}

func TestListOpenInvalidQuery(t *testing.T) {
	h := newHarness(t)
	for _, query := range []string{
		"supplierId=kopi",
		"lat=1.3",
		"lng=103.7",
		"lat=91&lng=0",
		"lat=0&lng=-180.5",
		"lat=north&lng=0",
		"lat=NaN&lng=0",
		"limit=0",
		"limit=101",
		"limit=ten",
		"cursor=" + url.QueryEscape("not a cursor"),
		"cursor=" + strings.Repeat("a", 513),
		"cursor=eyJmIjoiYWJjIn0", // {"f":"abc"}
	} {
		t.Run(query, func(t *testing.T) {
			requireError(t, h.get(t, h.other, "/errands/open?"+query), http.StatusBadRequest, apperror.InvalidArgument)
		})
	}
}

func TestListOpenCache(t *testing.T) {
	t.Run("serves the cached set until it expires", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(openErrand(1, time.Hour))
		assert.Equal(t, []string{errandID(1)}, ids(h.list(t, nil)))

		h.store.Put(assigned(openErrand(1, time.Hour), errand.StatusAssigned))
		h.store.Put(openErrand(2, time.Hour))
		h.clock.Advance(browse.OpenSetTTL - time.Millisecond)
		assert.Equal(t, []string{errandID(1)}, ids(h.list(t, nil)), "up to two seconds stale")
		assert.EqualValues(t, 1, h.store.listOpen.Load())

		h.clock.Advance(time.Millisecond)
		assert.Equal(t, []string{errandID(2)}, ids(h.list(t, nil)))
		assert.EqualValues(t, 2, h.store.listOpen.Load())

		sets := h.cache.CallsTo("Set")
		require.Len(t, sets, 2)
		assert.Equal(t, []any{1, browse.OpenSetTTL}, sets[0].Args)
	})

	t.Run("a cached errand is hidden once its deadline passes", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(openErrand(1, time.Second))
		h.store.Put(openErrand(2, time.Hour))
		assert.Len(t, h.list(t, nil).Items, 2)
		h.clock.Advance(time.Second)
		assert.Equal(t, []string{errandID(2)}, ids(h.list(t, nil)))
		assert.EqualValues(t, 1, h.store.listOpen.Load(), "still served from the cache")
	})

	t.Run("a cache failure is a miss", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(openErrand(1, time.Hour))
		h.cache.FailAlways("Get", errors.New("redis down"))
		assert.Equal(t, []string{errandID(1)}, ids(h.list(t, nil)))
		assert.Equal(t, []string{errandID(1)}, ids(h.list(t, nil)))
		assert.EqualValues(t, 2, h.store.listOpen.Load())
	})

	t.Run("concurrent misses load once", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(openErrand(1, time.Hour))
		h.list(t, nil) // warm the JWKS cache, so every request below is quick
		h.cache.Clear()
		h.store.listOpen.Store(0)
		h.store.gate = make(chan struct{})

		const n = 10
		var wg sync.WaitGroup
		results := make([][]string, n)
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i] = ids(h.list(t, nil))
			}()
		}
		require.Eventually(t, func() bool { return h.store.listOpen.Load() == 1 }, time.Second, time.Millisecond)
		time.Sleep(200 * time.Millisecond) // let the others join the load
		close(h.store.gate)
		wg.Wait()

		assert.EqualValues(t, 1, h.store.listOpen.Load())
		for _, r := range results {
			assert.Equal(t, []string{errandID(1)}, r)
		}
	})
}

func TestListOpenSupplierErrors(t *testing.T) {
	t.Run("supplier-service down is 503 and nothing is cached", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(openErrand(1, time.Hour))
		h.suppliers.FailNext("GetVersion", &apperror.Error{Code: apperror.DependencyUnavailable, Message: "down"})
		requireError(t, h.get(t, h.other, "/errands/open"), http.StatusServiceUnavailable, apperror.DependencyUnavailable)
		assert.Empty(t, h.cache.CallsTo("Set"))
		assert.Len(t, h.list(t, nil).Items, 1)
	})

	t.Run("an errand with an unknown version is hidden and logged", func(t *testing.T) {
		h := newHarness(t)
		h.store.Put(openErrand(1, time.Hour))
		broken := openErrand(2, time.Hour)
		broken.Details.SupplierVersionID = "9d9d9d9d-9d9d-4d9d-9d9d-9d9d9d9d9d9d"
		h.store.Put(broken)
		assert.Equal(t, []string{errandID(1)}, ids(h.list(t, nil)))
		assert.Contains(t, h.logs.String(), "unknown supplier version")
		assert.Contains(t, h.logs.String(), errandID(2))
	})

	t.Run("each version is read once per load", func(t *testing.T) {
		h := newHarness(t)
		for n := 1; n <= 3; n++ {
			h.store.Put(openErrand(n, time.Hour))
		}
		h.list(t, nil)
		assert.Len(t, h.suppliers.CallsTo("GetVersion"), 1)
	})
}

// ---------------------------------------------------------------------------
// GET /errands/{errandId}
// ---------------------------------------------------------------------------

func TestGetErrandViews(t *testing.T) {
	h := newHarness(t)
	open := openErrand(1, time.Hour)
	h.store.Put(open)
	h.store.Put(assigned(openErrand(2, time.Hour), errand.StatusPickedUp))
	h.store.Put(assigned(openErrand(3, time.Hour), errand.StatusCompleted))

	decode := func(recorder *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		var out map[string]any
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &out))
		return out
	}

	t.Run("anyone else sees an OPEN errand's public view", func(t *testing.T) {
		recorder := h.get(t, h.other, "/errands/"+strings.ToUpper(errandID(1)))
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		assert.JSONEq(t, `{
			"errandId": "`+errandID(1)+`",
			"version": 2,
			"supplierId": "`+kopiID+`",
			"supplierVersionId": "`+kopiVersionID+`",
			"pickup": {"name": "Kopi Corner", "building": "COM1", "floor": "1",
				"locationDescription": "next to the lift", "latitude": 1.2949, "longitude": 103.7737},
			"deliveryBuilding": {"id": "`+buildingID+`", "name": "COM1"},
			"deliveryDetail": "Level 2 lounge",
			"items": [{"description": "iced latte", "quantity": 2}],
			"reward": 10,
			"createdAt": "2026-03-02T08:40:00Z",
			"acceptanceDeadline": "2026-03-02T10:00:00Z",
			"viewerRole": "PUBLIC"
		}`, recorder.Body.String())
	})

	t.Run("the requester sees their view in any state", func(t *testing.T) {
		for _, id := range []string{errandID(1), errandID(2), errandID(3)} {
			got := decode(h.get(t, h.requester, "/errands/"+id))
			assert.Equal(t, "REQUESTER", got["viewerRole"])
			assert.Equal(t, "less ice", got["notes"])
			assert.Equal(t, contactName, got["contactName"])
			assert.NotContains(t, got, "courierId")
			assert.NotContains(t, got, "requesterId")
		}
	})

	t.Run("the assigned courier sees the contact only while assigned", func(t *testing.T) {
		got := decode(h.get(t, h.courier, "/errands/"+errandID(2)))
		assert.Equal(t, "COURIER", got["viewerRole"])
		assert.Equal(t, "less ice", got["notes"])
		assert.Equal(t, contactName, got["contactName"])
		assert.Equal(t, contactMobile, got["contactMobile"])
		assert.Equal(t, "2026-03-02T10:00:00Z", got["pickupDeadline"])
		assert.NotContains(t, got, "requesterId")

		got = decode(h.get(t, h.courier, "/errands/"+errandID(3)))
		assert.Equal(t, "COURIER", got["viewerRole"])
		assert.NotContains(t, got, "contactName")
		assert.NotContains(t, got, "contactMobile")
	})

	t.Run("anyone else gets 404 once the errand left OPEN", func(t *testing.T) {
		for _, id := range []string{errandID(2), errandID(3)} {
			requireError(t, h.get(t, h.other, "/errands/"+id), http.StatusNotFound, apperror.ErrandNotFound)
		}
	})

	t.Run("a courier who withdrew sees only the public view", func(t *testing.T) {
		got := decode(h.get(t, h.courier, "/errands/"+errandID(1)))
		assert.Equal(t, "PUBLIC", got["viewerRole"])
	})

	assert.Empty(t, h.cache.Calls(), "the detail route never reads the cache")
}

func TestGetErrandErrors(t *testing.T) {
	h := newHarness(t)
	h.store.Put(openErrand(1, time.Hour))

	requireError(t, h.get(t, h.other, "/errands/"+errandID(9)), http.StatusNotFound, apperror.ErrandNotFound)
	requireError(t, h.get(t, h.other, "/errands/not-a-uuid"), http.StatusBadRequest, apperror.InvalidArgument)

	h.suppliers.FailNext("GetVersion", &apperror.Error{Code: apperror.DependencyUnavailable, Message: "down"})
	requireError(t, h.get(t, h.other, "/errands/"+errandID(1)), http.StatusServiceUnavailable, apperror.DependencyUnavailable)

	h.suppliers.FailNext("GetVersion", &apperror.Error{Code: apperror.SupplierUnavailable, Message: "no such version"})
	requireError(t, h.get(t, h.requester, "/errands/"+errandID(1)), http.StatusInternalServerError, apperror.Internal)

	h.store.FailNext("Get", errors.New("connection reset"))
	requireError(t, h.get(t, h.requester, "/errands/"+errandID(1)), http.StatusInternalServerError, apperror.Internal)
	assert.NotContains(t, h.logs.String(), contactMobile)
}

func TestAuthorization(t *testing.T) {
	h := newHarness(t)
	h.store.Put(openErrand(1, time.Hour))
	admin := tokenFor(t, h.fixture, otherID, auth.ReviewErrands)
	for _, path := range []string{"/errands/open", "/errands/" + errandID(1)} {
		requireError(t, h.get(t, "", path), http.StatusUnauthorized, apperror.Unauthenticated)
		requireError(t, h.get(t, admin, path), http.StatusForbidden, apperror.Forbidden)
	}
}
