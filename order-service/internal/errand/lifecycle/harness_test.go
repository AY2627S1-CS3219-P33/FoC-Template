package lifecycle_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/auth/authtest"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/errandrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand/accept"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand/browse"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand/courier"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand/create"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand/mine"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand/requester"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/httpapi"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/testfakes"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/migrations"
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
)

var t0 = time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)

// records reads what a store wrote, for assertions. The fake and the
// PostgreSQL store each have one.
type records interface {
	// events lists the errand's errand_transitions events in order.
	events(t *testing.T, errandID string) []string
	// outbox lists the errand's outbox kinds in insertion order.
	outbox(t *testing.T, errandID string) []string
}

// world is order-service's user-facing errand API (A2–A7) on one router,
// over a store and fakes of the other ports. The credit-result consumer is
// played by reserve, the deadline jobs by claim.
type world struct {
	router    http.Handler
	store     errand.Store
	records   records
	suppliers *testfakes.FakeSupplierGate
	settings  *testfakes.FakeSettings
	clock     *testfakes.FakeClock
	fixture   *authtest.Fixture

	requester string
	courier   string
	rival     string
}

func newWorld(t *testing.T, store errand.Store, rec records) *world {
	t.Helper()
	f := authtest.New(t)
	cfg := auth.DefaultAuth0Config()
	cfg.Issuer = f.Server.URL + "/"
	verifier, err := auth.NewAuth0(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), auth.WithHTTPClient(f.Server.Client()))
	require.NoError(t, err)
	guard := auth.NewMiddleware(verifier)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	suppliers := testfakes.NewFakeSupplierGate()
	suppliers.AddSupplier(errand.SupplierVersion{
		SupplierID: supplierID, VersionID: versionID, Name: "Kopi Corner",
		Building: "COM1", Floor: "1", LocationDescription: "next to the lift",
		Latitude: 1.2949, Longitude: 103.7737, OpeningTime: "08:00", ClosingTime: "20:00",
	})
	suppliers.SetBuildings(errand.Building{ID: buildingID, Name: "COM1"})
	settings := testfakes.NewFakeSettings()
	clock := testfakes.NewFakeClock(t0)

	router := httpapi.NewRouter(httpapi.Dependencies{Logger: logger},
		create.NewHandler(create.Dependencies{Store: store, Suppliers: suppliers, Clock: clock, Logger: logger}, guard),
		browse.NewHandler(browse.Dependencies{
			Store: store, Suppliers: suppliers, Cache: testfakes.NewFakeOpenErrandCache(clock), Clock: clock, Logger: logger,
		}, guard),
		accept.NewHandler(accept.Dependencies{Store: store, Suppliers: suppliers, Settings: settings, Clock: clock, Logger: logger}, guard),
		requester.NewHandler(requester.Dependencies{Store: store, Suppliers: suppliers, Clock: clock, Logger: logger}, guard),
		courier.NewHandler(courier.Dependencies{Store: store, Suppliers: suppliers, Settings: settings, Clock: clock, Logger: logger}, guard),
		mine.NewHandler(mine.Dependencies{Store: store, Suppliers: suppliers, Logger: logger}, guard),
	)
	return &world{
		router: router, store: store, records: rec,
		suppliers: suppliers, settings: settings, clock: clock, fixture: f,
		requester: tokenFor(t, f, requesterID, auth.UseErrands),
		courier:   tokenFor(t, f, courierID, auth.UseErrands),
		rival:     tokenFor(t, f, rivalID, auth.UseErrands),
	}
}

// newFakeWorld runs on testfakes.FakeStore.
func newFakeWorld(t *testing.T) (*world, *testfakes.FakeStore) {
	t.Helper()
	store := testfakes.NewFakeStore()
	return newWorld(t, store, fakeRecords{store}), store
}

// newPostgresWorld runs on errandrepo.Store in a fresh schema of
// TEST_DATABASE_URL, and skips without it.
func newPostgresWorld(t *testing.T) *world {
	t.Helper()
	pool := newPool(t)
	return newWorld(t, errandrepo.New(pool), pgRecords{pool})
}

// forEachStore runs test on the fake store, and on PostgreSQL when
// TEST_DATABASE_URL is set.
func forEachStore(t *testing.T, test func(t *testing.T, w *world)) {
	t.Run("fake store", func(t *testing.T) {
		w, _ := newFakeWorld(t)
		test(t, w)
	})
	t.Run("postgres", func(t *testing.T) {
		test(t, newPostgresWorld(t))
	})
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

// ---------------------------------------------------------------------------
// Requests
// ---------------------------------------------------------------------------

func (w *world) do(t *testing.T, method, target, token, body string, headers ...string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, target, reader)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		request.Header.Set(headers[i], headers[i+1])
	}
	recorder := httptest.NewRecorder()
	w.router.ServeHTTP(recorder, request)
	return recorder
}

func (w *world) get(t *testing.T, token, target string) *httptest.ResponseRecorder {
	t.Helper()
	return w.do(t, http.MethodGet, target, token, "")
}

// act posts a VersionedAction: accept, cancel, confirm, withdraw, pickup or
// deliver.
func (w *world) act(t *testing.T, token, id, action string, version int) *httptest.ResponseRecorder {
	t.Helper()
	return w.do(t, http.MethodPost, "/errands/"+id+"/"+action, token, fmt.Sprintf(`{"expectedVersion":%d}`, version))
}

// reasoned posts a ReasonedAction: dispute or report-issue.
func (w *world) reasoned(t *testing.T, token, id, action string, version int, reason string) *httptest.ResponseRecorder {
	t.Helper()
	return w.do(t, http.MethodPost, "/errands/"+id+"/"+action, token, mustJSON(t, map[string]any{"expectedVersion": version, "reason": reason}))
}

// edit sends PATCH /errands/{id} with expectedVersion and fields.
func (w *world) edit(t *testing.T, token, id string, version int, fields map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	body := map[string]any{"expectedVersion": version}
	for k, v := range fields {
		body[k] = v
	}
	return w.do(t, http.MethodPatch, "/errands/"+id, token, mustJSON(t, body))
}

// create submits a valid errand as the requester and returns its ID. It is
// PENDING_RESERVATION at version 1.
func (w *world) create(t *testing.T) string {
	t.Helper()
	body := mustJSON(t, map[string]any{
		"supplierId":         supplierID,
		"deliveryBuildingId": buildingID,
		"deliveryDetail":     "Level 2 lounge",
		"items":              []any{map[string]any{"description": "iced latte", "quantity": 2}},
		"notes":              "less ice",
		"contactName":        contactName,
		"contactMobile":      contactMobile,
		"reward":             10,
	})
	recorder := w.do(t, http.MethodPost, "/errands", w.requester, body, "Idempotency-Key", newUUID(t))
	view := decode(t, recorder, http.StatusAccepted)
	require.Equal(t, "PENDING_RESERVATION", view["status"])
	return view["errandId"].(string)
}

// system applies a system event the way the credit-result consumer (C)
// would: errand.Next, then Store.Apply.
func (w *world) system(t *testing.T, id string, ev errand.Event) errand.Errand {
	t.Helper()
	e := w.errand(t, id)
	tr, err := errand.Next(e, ev, errand.SystemActor(), errand.EventInput{}, w.clock.Now())
	require.NoError(t, err)
	stored, err := w.store.Apply(context.Background(), tr)
	require.NoError(t, err)
	return stored
}

// reserve is credit-service confirming the reservation: OPEN at version 2.
func (w *world) reserve(t *testing.T, id string) {
	t.Helper()
	require.Equal(t, errand.StatusOpen, w.system(t, id, errand.EventReservationConfirmed).Status)
}

// claim runs one batch of a deadline job at now, deciding with errand.Next
// as the scheduler (B) does: a refusal skips the errand.
func (w *world) claim(t *testing.T, kind errand.DueKind, ev errand.Event, now time.Time) int {
	t.Helper()
	settings, err := w.settings.Get(context.Background())
	require.NoError(t, err)
	applied, err := w.store.ClaimDue(context.Background(), kind, now, 10, func(e errand.Errand) (errand.Transition, bool, error) {
		tr, err := errand.Next(e, ev, errand.SystemActor(), errand.EventInput{Deadlines: settings.Deadlines}, now)
		if err != nil {
			return errand.Transition{}, false, nil
		}
		return tr, true, nil
	})
	require.NoError(t, err)
	return applied
}

// advance takes a new errand through the API to status, which must be on
// the happy path: OPEN (version 2), ASSIGNED to courierID (3), PICKED_UP
// (4), DELIVERED (5) or COMPLETED (6).
func (w *world) advance(t *testing.T, to errand.Status) string {
	t.Helper()
	id := w.create(t)
	w.reserve(t, id)
	steps := []struct {
		token  string
		action string
	}{
		{w.courier, "accept"},
		{w.courier, "pickup"},
		{w.courier, "deliver"},
		{w.requester, "confirm"},
	}
	version := 2
	for _, step := range steps {
		if w.errand(t, id).Status == to {
			break
		}
		decode(t, w.act(t, step.token, id, step.action, version), http.StatusOK)
		version++
	}
	require.Equal(t, to, w.errand(t, id).Status)
	return id
}

// forgetWithdrawals moves the clock past the withdrawal restriction's
// window, so tests that withdraw courierID repeatedly do not restrict them
// (F3.6.4).
func (w *world) forgetWithdrawals() {
	w.clock.Advance(errand.DefaultSystemSettings().WithdrawalRestriction.Window + time.Second)
}

func (w *world) errand(t *testing.T, id string) errand.Errand {
	t.Helper()
	e, err := w.store.Get(context.Background(), id)
	require.NoError(t, err)
	return e
}

// ---------------------------------------------------------------------------
// Responses
// ---------------------------------------------------------------------------

func decode(t *testing.T, recorder *httptest.ResponseRecorder, status int) map[string]any {
	t.Helper()
	require.Equal(t, status, recorder.Code, recorder.Body.String())
	var body map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body), recorder.Body.String())
	return body
}

func requireError(t *testing.T, recorder *httptest.ResponseRecorder, status int, code apperror.Code) {
	t.Helper()
	require.Equal(t, status, recorder.Code, recorder.Body.String())
	require.Equal(t, code, errorCode(t, recorder), recorder.Body.String())
}

func errorCode(t *testing.T, recorder *httptest.ResponseRecorder) apperror.Code {
	t.Helper()
	var body apperror.Error
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body), recorder.Body.String())
	return body.Code
}

// listIDs returns the errand IDs of one /me/errands or /errands/open page.
func listIDs(t *testing.T, recorder *httptest.ResponseRecorder) []string {
	t.Helper()
	var page struct {
		Items []struct {
			ErrandID string `json:"errandId"`
		} `json:"items"`
	}
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &page))
	ids := make([]string, 0, len(page.Items))
	for _, item := range page.Items {
		ids = append(ids, item.ErrandID)
	}
	return ids
}

func listItems(t *testing.T, recorder *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	var page struct {
		Items []map[string]any `json:"items"`
	}
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &page))
	return page.Items
}

func count(values []string, want string) int {
	n := 0
	for _, v := range values {
		if v == want {
			n++
		}
	}
	return n
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return string(raw)
}

func newUUID(t *testing.T) string {
	t.Helper()
	value := make([]byte, 16)
	_, err := rand.Read(value)
	require.NoError(t, err)
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}

// ---------------------------------------------------------------------------
// Records
// ---------------------------------------------------------------------------

type fakeRecords struct{ store *testfakes.FakeStore }

func (r fakeRecords) events(_ *testing.T, errandID string) []string {
	var out []string
	for _, tr := range r.store.Transitions() {
		if tr.ErrandID == errandID {
			out = append(out, string(tr.Event))
		}
	}
	return out
}

func (r fakeRecords) outbox(_ *testing.T, errandID string) []string {
	var out []string
	for _, row := range r.store.Outbox() {
		if row.ErrandID == errandID {
			out = append(out, row.Kind)
		}
	}
	return out
}

type pgRecords struct{ pool *pgxpool.Pool }

func (r pgRecords) events(t *testing.T, errandID string) []string {
	return r.column(t, `SELECT event FROM errand_transitions WHERE errand_id = $1 ORDER BY id`, errandID)
}

func (r pgRecords) outbox(t *testing.T, errandID string) []string {
	return r.column(t, `SELECT kind FROM outbox WHERE errand_id = $1 ORDER BY id`, errandID)
}

func (r pgRecords) column(t *testing.T, query, errandID string) []string {
	t.Helper()
	rows, err := r.pool.Query(context.Background(), query, errandID)
	require.NoError(t, err)
	values, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	return values
}

// newPool creates a schema of its own in TEST_DATABASE_URL, migrates it and
// drops it after the test, as errandrepo's tests do.
func newPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set; skipping PostgreSQL lifecycle test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	admin, err := pgx.Connect(ctx, databaseURL)
	require.NoError(t, err)
	schema := "order_lifecycle_" + strings.ReplaceAll(newUUID(t), "-", "")
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	_, err = admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema)
	require.NoError(t, err)

	config, err := pgxpool.ParseConfig(databaseURL)
	require.NoError(t, err)
	config.ConnConfig.RuntimeParams["search_path"] = schema
	config.MaxConns = 16
	pool, err := pgxpool.NewWithConfig(ctx, config)
	require.NoError(t, err)
	t.Cleanup(func() {
		pool.Close()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		_, _ = admin.Exec(cleanupCtx, "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
		_ = admin.Close(cleanupCtx)
	})

	up, err := migrations.Up()
	require.NoError(t, err)
	_, err = pool.Exec(ctx, up)
	require.NoError(t, err)
	return pool
}
