package app_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/app"
	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/auth/authtest"
	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/config"
	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/enrichment"
	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/events"
	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/migrations"
)

const (
	errandID = "6f1c2b0e-8d4a-4e1b-9c3f-2a7d5e8b9c10"
	supplier = "11111111-1111-4111-8111-111111111111"
	building = "22222222-2222-4222-8222-222222222222"
)

// fakeOrderService serves the errand export the way order-service does, and
// records what it was asked.
type fakeOrderService struct {
	mu           sync.Mutex
	down         bool
	supplierName *string
	auth         []string
	ids          []string
}

func (f *fakeOrderService) setDown(down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.down = down
}

func (f *fakeOrderService) setSupplierName(name *string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.supplierName = name
}

func (f *fakeOrderService) requests() (auth, ids []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.auth...), append([]string(nil), f.ids...)
}

func (f *fakeOrderService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.ids = append(f.ids, r.URL.Query().Get("ids"))
	if f.down {
		http.Error(w, `{"code":"INTERNAL","message":"x"}`, http.StatusServiceUnavailable)
		return
	}
	item := map[string]any{
		"errandId": errandID, "status": "ASSIGNED", "version": 3, "courierId": "auth0|cou1", "reward": 8,
		"supplierId": supplier, "supplierName": f.supplierName, "deliveryBuildingId": building,
		"deliveryBuildingName": "COM1", "createdAt": "2026-03-02T09:00:00Z", "updatedAt": "2026-03-02T10:00:00Z",
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{item}})
}

func envelope(eventType string, version int, from, to string) []byte {
	fromField := ""
	if from != "" {
		fromField = fmt.Sprintf(`"from": %q,`, from)
	}
	return []byte(fmt.Sprintf(`{"messageId": "%s:%s:%d", "type": %q, "occurredAt": "2026-03-02T09:%02d:00Z",
		"errandId": %q, "errandVersion": %d,
		"data": {"event": "x", %s "to": %q, "courierId": "auth0|cou1", "reward": 8}}`,
		errandID, eventType[len("errand."):], version, eventType, version, errandID, version, fromField, to))
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

type factRow struct {
	status        string
	version       int
	supplierName  *string
	supplierID    *string
	buildingName  *string
	enrichmentDue bool
	terminal      *string
}

func TestAdminServiceRecordsEventsAndEnrichesErrands(t *testing.T) {
	databaseURL, amqpURL := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_AMQP_URL")
	if databaseURL == "" || amqpURL == "" {
		t.Skip("TEST_DATABASE_URL and TEST_AMQP_URL are required; skipping the admin-service integration test")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// A schema with the migrations applied; the service connects with it as
	// its search path.
	admin, err := pgx.Connect(ctx, databaseURL)
	require.NoError(t, err)
	suffix := make([]byte, 8)
	_, _ = rand.Read(suffix)
	schema := "admin_app_" + hex.EncodeToString(suffix)
	_, err = admin.Exec(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
		_ = admin.Close(context.Background())
	})
	_, err = admin.Exec(ctx, "SET search_path TO "+schema)
	require.NoError(t, err)
	up, err := migrations.Up()
	require.NoError(t, err)
	_, err = admin.Exec(ctx, up)
	require.NoError(t, err)
	serviceURL, err := url.Parse(databaseURL)
	require.NoError(t, err)
	query := serviceURL.Query()
	query.Set("search_path", schema)
	serviceURL.RawQuery = query.Encode()

	// A clean queue, deleted afterwards.
	conn, err := amqp.Dial(amqpURL)
	require.NoError(t, err)
	ch, err := conn.Channel()
	require.NoError(t, err)
	require.NoError(t, events.DeclareTopology(ch))
	_, err = ch.QueuePurge(events.Queue, false)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = ch.QueueDelete(events.Queue, false, false, false)
		_, _ = ch.QueueDelete(events.QueueDLQ, false, false, false)
		_ = conn.Close()
	})

	// A fake Auth0 tenant (the token endpoint) and a fake order-service.
	tenant := authtest.New(t)
	tenant.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("client_id") != "admin-m2m" || r.Form.Get("audience") != config.OrderAudience {
			http.Error(w, "bad client", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"m2m-token","token_type":"Bearer","expires_in":3600}`))
	})
	order := &fakeOrderService{}
	orderServer := httptest.NewServer(order)
	t.Cleanup(orderServer.Close)
	// user-service answers /readyz with only {"status":"ready"}.
	userServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	}))
	t.Cleanup(userServer.Close)
	port := freePort(t)

	cfg, err := config.LoadFrom(func(key string) (string, bool) {
		value, ok := map[string]string{
			"HTTP_HOST": "127.0.0.1", "HTTP_PORT": strconv.Itoa(port),
			"DATABASE_URL":        serviceURL.String(),
			"AUTH0_ISSUER":        tenant.Server.URL + "/",
			"AUTH0_AUDIENCE":      "https://api.foc.local/admin-service",
			"ADMIN_M2M_CLIENT_ID": "admin-m2m", "ADMIN_M2M_CLIENT_SECRET": "secret",
			"ORDER_SERVICE_URL": orderServer.URL, "AMQP_URL": amqpURL, "USER_SERVICE_URL": userServer.URL,
			"SHUTDOWN_TIMEOUT": "5s",
		}[key]
		return value, ok
	})
	require.NoError(t, err)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	application, err := app.New(ctx, cfg, logger,
		app.WithTrustedClient(tenant.Server.Client()),
		app.WithEnrichment(enrichment.Config{Interval: 100 * time.Millisecond, RetryAfter: 200 * time.Millisecond}))
	require.NoError(t, err)
	defer application.Close()
	running := make(chan error, 1)
	go func() { running <- application.Run(ctx) }()

	readyz := func() (int, map[string]any) {
		recorder := httptest.NewRecorder()
		application.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		var body map[string]any
		_ = json.Unmarshal(recorder.Body.Bytes(), &body)
		return recorder.Code, body
	}
	row := func() (factRow, bool) {
		var r factRow
		err := admin.QueryRow(ctx, `
			SELECT status, version, supplier_name, supplier_id::text, delivery_building_name, enrichment_due, terminal_status
			FROM errand_facts WHERE errand_id = $1`, errandID).Scan(
			&r.status, &r.version, &r.supplierName, &r.supplierID, &r.buildingName, &r.enrichmentDue, &r.terminal)
		return r, err == nil
	}
	publish := func(eventType string, version int, from, to string) {
		require.NoError(t, ch.PublishWithContext(ctx, events.ExchangeEvents, eventType, false, false, amqp.Publishing{
			ContentType: "application/json", DeliveryMode: amqp.Persistent, Body: envelope(eventType, version, from, to),
		}))
	}

	// Ready once the consumer has subscribed.
	require.Eventually(t, func() bool {
		code, body := readyz()
		return code == http.StatusOK && body["status"] == "ready"
	}, 10*time.Second, 50*time.Millisecond)
	_, body := readyz()
	require.Equal(t, map[string]any{"postgres": "ok", "events": "running"}, body["dependencies"])

	// supplier-service is down for order-service: the name comes back null.
	order.setSupplierName(nil)
	publish("errand.placed", 1, "", "PENDING_RESERVATION")
	publish("errand.opened", 2, "PENDING_RESERVATION", "OPEN")
	publish("errand.accepted", 3, "OPEN", "ASSIGNED")

	require.Eventually(t, func() bool {
		r, ok := row()
		return ok && r.status == "ASSIGNED" && r.supplierID != nil && *r.supplierID == supplier
	}, 10*time.Second, 50*time.Millisecond, "the accepted event enriched the errand from the export")
	r, _ := row()
	require.Nil(t, r.supplierName, "a null supplier name is stored as unknown")
	require.Equal(t, "COM1", *r.buildingName)
	require.True(t, r.enrichmentDue, "and retried later")

	// The retry finds the name once supplier-service is back.
	order.setSupplierName(strPtr("Kopi Corner"))
	require.Eventually(t, func() bool {
		r, _ := row()
		return r.supplierName != nil && !r.enrichmentDue
	}, 10*time.Second, 50*time.Millisecond)
	auth, ids := order.requests()
	for _, header := range auth {
		require.Equal(t, "Bearer m2m-token", header, "every export call carries the machine-to-machine token")
	}
	for _, requested := range ids {
		require.Equal(t, errandID, requested)
	}

	// order-service is down when the errand finishes: the event is still
	// recorded, and the errand waits for enrichment.
	order.setDown(true)
	publish("errand.completed", 8, "DELIVERED", "COMPLETED")
	require.Eventually(t, func() bool {
		r, ok := row()
		return ok && r.status == "COMPLETED" && r.terminal != nil && r.enrichmentDue
	}, 10*time.Second, 50*time.Millisecond)

	// It recovers without anyone intervening.
	order.setDown(false)
	require.Eventually(t, func() bool {
		r, _ := row()
		return !r.enrichmentDue
	}, 10*time.Second, 50*time.Millisecond)

	// The metrics read what the events built, through the real auth guard.
	get := func(path, token string) (int, map[string]any) {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		recorder := httptest.NewRecorder()
		application.Handler().ServeHTTP(recorder, request)
		var body map[string]any
		_ = json.Unmarshal(recorder.Body.Bytes(), &body)
		return recorder.Code, body
	}
	adminToken := tenant.Token(t, tenant.Claims("https://api.foc.local/admin-service", []string{"dashboard:read"}), "one")
	// The events happened at 09:xx UTC on 2026-03-02, which is 17:xx there.
	code, outcomes := get("/v1/metrics/outcomes?from=2026-03-02&to=2026-03-02", adminToken)
	require.Equal(t, http.StatusOK, code, outcomes)
	require.EqualValues(t, 1, outcomes["finished"])
	require.EqualValues(t, 1, outcomes["outcomes"].([]any)[0].(map[string]any)["count"], "COMPLETED is first")
	code, suppliers := get("/v1/metrics/top-suppliers?from=2026-03-02&to=2026-03-02", adminToken)
	require.Equal(t, http.StatusOK, code, suppliers)
	top := suppliers["items"].([]any)[0].(map[string]any)
	require.Equal(t, supplier, top["id"])
	require.Equal(t, "Kopi Corner", top["name"], "the name enrichment stored")
	code, couriers := get("/v1/metrics/active-couriers?from=2026-03-02&to=2026-03-02", adminToken)
	require.Equal(t, http.StatusOK, code, couriers)
	require.EqualValues(t, 1, couriers["couriers"])
	code, _ = get("/v1/metrics/outcomes", "")
	require.Equal(t, http.StatusUnauthorized, code)
	userToken := tenant.Token(t, tenant.Claims("https://api.foc.local/admin-service", []string{"errands:use"}), "one")
	code, _ = get("/v1/metrics/outcomes", userToken)
	require.Equal(t, http.StatusForbidden, code, "a user without dashboard:read")

	// The same event twice more changes nothing.
	publish("errand.completed", 8, "DELIVERED", "COMPLETED")
	publish("errand.accepted", 3, "OPEN", "ASSIGNED")
	time.Sleep(300 * time.Millisecond)
	var facts int
	require.NoError(t, admin.QueryRow(ctx, `SELECT count(*) FROM transition_facts`).Scan(&facts))
	require.Equal(t, 4, facts, "placed, opened, accepted and completed; the redeliveries add nothing")

	// The readiness section: order-service, user-service and admin-service
	// itself are checked; supplier-service and credit-service are unconfigured.
	readiness := func() map[string]any {
		code, body := get("/v1/readiness", adminToken)
		require.Equal(t, http.StatusOK, code, body)
		return body
	}
	byName := func(body map[string]any) map[string]map[string]any {
		out := map[string]map[string]any{}
		for _, s := range body["services"].([]any) {
			service := s.(map[string]any)
			out[service["name"].(string)] = service
		}
		return out
	}
	body = readiness()
	services := byName(body)
	require.Len(t, services, 3)
	require.Equal(t, "up", services["order-service"]["state"])
	require.Equal(t, "up", services["user-service"]["state"])
	require.Equal(t, "ready", services["user-service"]["status"])
	require.Nil(t, services["user-service"]["dependencies"], "it reports only ready or not ready; none is invented")
	require.Equal(t, "up", services["admin-service"]["state"])
	require.Equal(t, map[string]any{"postgres": "ok", "events": "running"}, services["admin-service"]["dependencies"],
		"its own /readyz detail, read over the loopback")
	var notChecked []string
	for _, n := range body["notChecked"].([]any) {
		notChecked = append(notChecked, n.(map[string]any)["name"].(string))
	}
	require.ElementsMatch(t, []string{"supplier-service", "credit-service"}, notChecked)
	for _, n := range body["notChecked"].([]any) {
		reason := n.(map[string]any)["reason"].(string)
		require.Contains(t, reason, "is not configured", "an unconfigured service says why")
	}
	order.setDown(true)
	services = byName(readiness())
	require.Equal(t, "down", services["order-service"]["state"], "order-service answers 503")
	require.Equal(t, "not_ready", services["order-service"]["reason"])
	require.Equal(t, "up", services["admin-service"]["state"])

	cancel()
	select {
	case err := <-running:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("the service did not stop")
	}
}

func strPtr(s string) *string { return &s }
