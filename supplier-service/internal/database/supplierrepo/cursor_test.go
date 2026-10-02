package supplierrepo

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier/query"
)

const cursorVersionID supplier.VersionID = "173fd7be-4f54-4f15-a8b1-58bd502344a0"

func TestCursorIsOpaqueBoundedAndBoundToNormalizedFilters(t *testing.T) {
	r := New(nil)
	filter := supplier.ListFilter{Query: " COM\t 3 ", Type: " FOOD ", Limit: 1}
	token := r.encodeCursor(cursorVersionID, filter)
	require.LessOrEqual(t, len(token), 512)
	raw, err := base64.RawURLEncoding.DecodeString(token)
	require.NoError(t, err)
	require.NotContains(t, string(raw), string(cursorVersionID))
	id, err := r.decodeCursor(token, supplier.ListFilter{Query: "com 3", Type: "food", Limit: 100})
	require.NoError(t, err)
	require.Equal(t, cursorVersionID, id)
	for _, changed := range []supplier.ListFilter{{Query: "com 4", Type: "food"}, {Query: "com 3", Type: "shopping"}} {
		_, err := r.decodeCursor(token, changed)
		require.Error(t, err)
	}
	_, err = New(nil).decodeCursor(token, filter)
	require.Error(t, err, "a fresh repository requires a fresh traversal")
}

func TestInvalidAndTamperedCursorsFailBeforeDatabaseAccessAndReturn400(t *testing.T) {
	r := New(nil)
	token := r.encodeCursor(cursorVersionID, supplier.ListFilter{})
	raw, err := base64.RawURLEncoding.DecodeString(token)
	require.NoError(t, err)
	raw[len(raw)-1] ^= 1
	tampered := base64.RawURLEncoding.EncodeToString(raw)
	for _, value := range []string{
		"not-a-cursor!", "e30", token[:len(token)-8], tampered, token[:8] + "\n" + token[8:],
		strings.Repeat("x", 513), r.encodeCursor("invalid-version-id", supplier.ListFilter{}),
		base64.RawURLEncoding.EncodeToString([]byte(`{"name":"alpha","supplierId":"29e9aa8b-5651-4981-8828-3fbb1b21fcb1"}`)),
	} {
		t.Run(value, func(t *testing.T) {
			_, err := r.ListAvailable(context.Background(), supplier.ListFilter{Cursor: value})
			require.Error(t, err)
			router := http.NewServeMux()
			query.NewHandler(r).RegisterRoutes(router)
			request := httptest.NewRequest(http.MethodGet, "/suppliers?cursor="+url.QueryEscape(value), nil)
			request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{Subject: "user-1", Permissions: []auth.Permission{auth.ReadSuppliers}}))
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			require.Equal(t, http.StatusBadRequest, response.Code)
			require.Contains(t, response.Body.String(), string(apperror.InvalidArgument))
		})
	}
}
