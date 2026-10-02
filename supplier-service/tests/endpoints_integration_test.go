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
