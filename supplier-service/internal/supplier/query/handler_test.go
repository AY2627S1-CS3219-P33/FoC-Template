package query

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
)

func catalogueItem() supplier.Supplier {
	image := "https://example.com/supplier.png"
	return supplier.Supplier{
		SupplierID: testSupplierID, VersionID: "173fd7be-4f54-4f15-a8b1-58bd502344a0",
		Details: supplier.Details{
			Name: "The Deck", Type: "food", Building: "Arts Link", Floor: "2",
			LocationDescription: "Beside the library", Latitude: 1.294, Longitude: 103.772,
			OpeningTime: "08:00", ClosingTime: "20:00", ImageURL: &image,
		},
		Available: true,
		CreatedAt: time.Date(2026, 9, 30, 8, 15, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 10, 1, 8, 15, 0, 0, time.UTC),
	}
}

const itemJSON = `{
	"supplierId":"29e9aa8b-5651-4981-8828-3fbb1b21fcb1",
	"versionId":"173fd7be-4f54-4f15-a8b1-58bd502344a0",
	"name":"The Deck", "type":"food", "building":"Arts Link", "floor":"2",
	"locationDescription":"Beside the library", "latitude":1.294, "longitude":103.772,
	"openingTime":"08:00", "closingTime":"20:00", "imageUrl":"https://example.com/supplier.png",
	"available":true, "createdAt":"2026-09-30T08:15:00Z", "updatedAt":"2026-10-01T08:15:00Z"
}`

func TestCatalogueHandlersReturnAllF211FieldsAndBothIdentifiers(t *testing.T) {
	reader := &readerStub{item: catalogueItem(), page: supplier.Page{Items: []supplier.Supplier{catalogueItem()}, NextCursor: "opaque"}}
	response := serveCatalogue(reader, "/suppliers/"+string(testSupplierID), true)
	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, "application/json", response.Header().Get("Content-Type"))
	require.JSONEq(t, itemJSON, response.Body.String())
	response = serveCatalogue(reader, "/suppliers?q=++ARTS+++LINK++&type=FOOD&limit=100&cursor=previous", true)
	require.Equal(t, http.StatusOK, response.Code)
	require.JSONEq(t, `{"items":[`+itemJSON+`],"nextCursor":"opaque"}`, response.Body.String())
	require.Equal(t, supplier.ListFilter{Query: "arts link", Type: "food", Limit: 100, Cursor: "previous"}, reader.filter)
}

func TestListReturnsEmptyArrayAndOmitsFinalCursor(t *testing.T) {
	reader := &readerStub{}
	response := serveCatalogue(reader, "/suppliers", true)
	require.Equal(t, http.StatusOK, response.Code)
	require.JSONEq(t, `{"items":[]}`, response.Body.String())
	require.Equal(t, supplier.DefaultPageSize, reader.filter.Limit)
	reader.page.Items = []supplier.Supplier{catalogueItem()}
	reader.page.Items[0].Details.ImageURL = nil
	response = serveCatalogue(reader, "/suppliers?limit=1", true)
	require.Equal(t, http.StatusOK, response.Code)
	require.NotContains(t, response.Body.String(), "nextCursor")
	require.NotContains(t, response.Body.String(), "imageUrl")
}

func TestCatalogueHandlersRejectClientIdentityWithoutTrustedContext(t *testing.T) {
	for _, target := range []string{
		"/suppliers?subject=user-1&role=administrator&limit=invalid",
		"/suppliers/" + string(testSupplierID) + "?role=administrator",
	} {
		reader := &readerStub{}
		response := serveCatalogue(reader, target, false)
		require.Equal(t, http.StatusUnauthorized, response.Code)
		require.JSONEq(t, `{"code":"UNAUTHENTICATED","message":"authentication is required"}`, response.Body.String())
		require.Zero(t, reader.calls)
	}
	reader := &readerStub{}
	router := http.NewServeMux()
	NewHandler(reader).RegisterRoutes(router)
	request := httptest.NewRequest(http.MethodGet, "/suppliers", nil)
	request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{Roles: []auth.Role{auth.RoleAdministrator}}))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusUnauthorized, response.Code)
	require.Zero(t, reader.calls)
}

func TestCatalogueHandlersRejectInvalidParametersBeforeReading(t *testing.T) {
	for _, target := range []string{
		"/suppliers?limit=0", "/suppliers?limit=-1", "/suppliers?limit=101",
		"/suppliers?limit=", "/suppliers?limit=1.5", "/suppliers?limit=text",
		"/suppliers?limit=999999999999999999999",
		"/suppliers?q=" + url.QueryEscape(strings.Repeat("界", 121)),
		"/suppliers?q=" + url.QueryEscape(strings.Repeat(" ", 121)),
		"/suppliers?type=" + strings.Repeat("x", 65),
		"/suppliers?type=" + url.QueryEscape(strings.Repeat(" ", 65)),
		"/suppliers?cursor=" + strings.Repeat("x", 513),
		"/suppliers/invalid",
	} {
		t.Run(target, func(t *testing.T) {
			reader := &readerStub{}
			response := serveCatalogue(reader, target, true)
			require.Equal(t, http.StatusBadRequest, response.Code)
			var result apperror.Error
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
			require.Equal(t, apperror.InvalidArgument, result.Code)
			if target != "/suppliers/invalid" {
				require.NotEmpty(t, result.Fields)
			}
			require.Zero(t, reader.calls)
		})
	}
}

func TestCatalogueHandlersMapReadFailures(t *testing.T) {
	for _, test := range []struct {
		name   string
		target string
		err    error
		status int
		code   apperror.Code
	}{
		{"deleted or missing", "/suppliers/" + string(testSupplierID), &apperror.Error{Code: apperror.SupplierNotFound, Message: "supplier not found"}, 404, apperror.SupplierNotFound},
		{"invalid cursor", "/suppliers?cursor=invalid", &apperror.Error{Code: apperror.InvalidArgument, Message: "cursor is invalid"}, 400, apperror.InvalidArgument},
		{"list dependency", "/suppliers", &apperror.Error{Code: apperror.DependencyUnavailable, Message: "database password is secret"}, 503, apperror.DependencyUnavailable},
		{"detail dependency", "/suppliers/" + string(testSupplierID), &apperror.Error{Code: apperror.DependencyUnavailable, Message: "database password is secret"}, 503, apperror.DependencyUnavailable},
		{"list internal", "/suppliers", errors.New("database password is secret"), 500, apperror.Internal},
		{"detail internal", "/suppliers/" + string(testSupplierID), errors.New("database password is secret"), 500, apperror.Internal},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := serveCatalogue(&readerStub{err: test.err}, test.target, true)
			require.Equal(t, test.status, response.Code)
			var result apperror.Error
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
			require.Equal(t, test.code, result.Code)
			require.NotContains(t, response.Body.String(), "secret")
		})
	}
}

func serveCatalogue(reader supplier.Reader, target string, authenticated bool) *httptest.ResponseRecorder {
	router := http.NewServeMux()
	NewHandler(reader).RegisterRoutes(router)
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.Header.Set("Authorization", "Bearer client-supplied-token")
	if authenticated {
		request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{Subject: "user-1"}))
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}
