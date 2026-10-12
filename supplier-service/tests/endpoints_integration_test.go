package tests

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/app"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/testauth"
)

func TestApplicationEndpoints_F2_1_F2_2_F2_3_F2_4_F2_5(t *testing.T) {
	databaseURL, conn := startupDatabase(t)
	f := testauth.New(t)
	cfg := startupConfig()
	cfg.Database.URL = databaseURL
	cfg.Auth.Issuer = f.Server.URL + "/"
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	application, err := app.New(context.Background(), cfg, logger, auth.WithHTTPClient(f.Server.Client()))
	require.NoError(t, err)
	t.Cleanup(application.Close)
	token := f.Token(t, f.Claims(auth.SupplierAudience, []string{"suppliers:read", "suppliers:manage"}), "one")
	request := func(method, path, body string, status int) map[string]any {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if path != "/readyz" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.Header.Set("Idempotency-Key", "00000000-0000-4000-8000-000000000003")
		rec := httptest.NewRecorder()
		application.Handler().ServeHTTP(rec, req)
		require.Equal(t, status, rec.Code, rec.Body.String())
		var response map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
		return response
	}
	request("GET", "/readyz", "", 200)
	page := request("GET", "/suppliers?limit=1", "", 200)
	require.Len(t, page["items"], 1)
	require.NotEmpty(t, page["nextCursor"])
	request("GET", "/suppliers?limit=1&cursor="+page["nextCursor"].(string), "", 200)
	created := request("POST", "/suppliers", createBody, 201)
	id, version := created["supplierId"].(string), created["versionId"].(string)
	require.Regexp(t, uuidPattern, created["buildingId"])
	require.Regexp(t, uuidPattern, created["typeId"])
	request("POST", "/suppliers", strings.Replace(createBody, "Fixture supplier", "  FIXTURE   supplier  ", 1), 409)
	current := request("GET", "/suppliers/"+id, "", 200)
	require.Equal(t, created, current)
	updated := request("PATCH", "/suppliers/"+id, `{"name":"Updated fixture","imageUrl":null}`, 200)
	require.Equal(t, id, updated["supplierId"])
	require.NotEqual(t, version, updated["versionId"])
	require.Equal(t, "Updated fixture", updated["name"])
	request("PATCH", "/suppliers/"+id, `{"supplierId":"00000000-0000-4000-8000-000000000099"}`, 400)
	old := request("GET", "/supplier-versions/"+version, "", 200)
	require.Equal(t, created["name"], old["name"])
	require.Equal(t, created["buildingId"], old["buildingId"])
	require.Equal(t, created["typeId"], old["typeId"])
	require.Equal(t, created["buildingId"], updated["buildingId"], "an unpatched building keeps its lookup")
	require.Equal(t, false, old["available"])
	current = request("GET", "/suppliers/"+id, "", 200)
	require.Equal(t, updated["versionId"], current["versionId"])
	page = request("GET", "/suppliers?q=Updated%20fixture&type=FOOD", "", 200)
	require.Len(t, page["items"], 1)

	// Missing B4 is represented as a dependency failure, never permission to delete.
	for range 2 {
		response := request("DELETE", "/suppliers/"+id, "", 503)
		require.Equal(t, "DELETION_FENCE_UNAVAILABLE", response["code"])
	}
	request("GET", "/suppliers/"+id, "", 200)
	request("GET", "/supplier-versions/"+version, "", 200)
	var operations int
	var state, failure string
	require.NoError(t, conn.QueryRow(context.Background(), "SELECT count(*) FROM supplier_deletion_operations").Scan(&operations))
	require.Equal(t, 1, operations)
	require.NoError(t, conn.QueryRow(context.Background(), "SELECT state, last_failure_code FROM supplier_deletion_operations").Scan(&state, &failure))
	require.Equal(t, "requested", state)
	require.Equal(t, "acquire_unavailable", failure)
}

const uuidPattern = `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`

func TestApplicationLookupEndpoints_F2_1_2_F2_2_1_F2_5_1(t *testing.T) {
	databaseURL, _ := startupDatabase(t)
	f := testauth.New(t)
	cfg := startupConfig()
	cfg.Database.URL = databaseURL
	cfg.Auth.Issuer = f.Server.URL + "/"
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	application, err := app.New(context.Background(), cfg, logger, auth.WithHTTPClient(f.Server.Client()))
	require.NoError(t, err)
	t.Cleanup(application.Close)
	admin := f.Token(t, f.Claims(auth.SupplierAudience, []string{"suppliers:read", "suppliers:manage"}), "one")
	reader := f.Token(t, f.Claims(auth.SupplierAudience, []string{"suppliers:read"}), "one")
	send := func(token, method, path, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		application.Handler().ServeHTTP(rec, req)
		require.Equal(t, status, rec.Code, rec.Body.String())
		return rec
	}
	decode := func(rec *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		var response map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
		return response
	}
	list := func(path string) map[string]map[string]any {
		t.Helper()
		byName := map[string]map[string]any{}
		for _, item := range decode(send(reader, "GET", path, "", 200))["items"].([]any) {
			row := item.(map[string]any)
			byName[row["name"].(string)] = row
		}
		return byName
	}

	send("", "GET", "/buildings", "", 401)
	send("", "GET", "/supplier-types", "", 401)

	for _, kind := range []struct {
		path, seeded, variant, conflict, inUse, notFound string
	}{
		{"/buildings", "Com 2", "Com2", "BUILDING_CONFLICT", "BUILDING_IN_USE", "BUILDING_NOT_FOUND"},
		{"/supplier-types", "Printing", " PRINTING ", "SUPPLIER_TYPE_CONFLICT", "SUPPLIER_TYPE_IN_USE", "SUPPLIER_TYPE_NOT_FOUND"},
	} {
		t.Run(kind.path, func(t *testing.T) {
			// The seed import deduplicated spellings into one live row.
			live := list(kind.path)
			seeded, found := live[kind.seeded]
			require.True(t, found, "seeded %s is listed", kind.seeded)
			require.Equal(t, false, seeded["retired"])
			require.Regexp(t, uuidPattern, seeded["id"])

			conflict := decode(send(admin, "POST", kind.path, `{"name":"`+kind.variant+`"}`, 409))
			require.Equal(t, kind.conflict, conflict["code"])
			require.Contains(t, conflict["message"], `"`+kind.seeded+`"`)
			send(reader, "POST", kind.path, `{"name":"Fixture Lookup"}`, 403)
			send(admin, "POST", kind.path, `{"name":"  "}`, 400)

			created := decode(send(admin, "POST", kind.path, `{"name":"  Fixture Lookup "}`, 201))
			require.Equal(t, "Fixture Lookup", created["name"])
			require.Equal(t, false, created["retired"])
			require.Contains(t, list(kind.path), "Fixture Lookup")

			inUse := decode(send(admin, "DELETE", kind.path+"/"+seeded["id"].(string), "", 409))
			require.Equal(t, kind.inUse, inUse["code"])
			send(reader, "DELETE", kind.path+"/"+created["id"].(string), "", 403)
			for range 2 {
				require.Empty(t, send(admin, "DELETE", kind.path+"/"+created["id"].(string), "", 204).Body.String())
			}
			require.NotContains(t, list(kind.path), "Fixture Lookup")
			retired := list(kind.path + "?includeRetired=true")["Fixture Lookup"]
			require.Equal(t, created["id"], retired["id"])
			require.Equal(t, true, retired["retired"])

			notFound := decode(send(admin, "DELETE", kind.path+"/00000000-0000-4000-8000-000000000099", "", 404))
			require.Equal(t, kind.notFound, notFound["code"])
		})
	}
}
