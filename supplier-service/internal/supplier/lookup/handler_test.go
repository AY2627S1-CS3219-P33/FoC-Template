package lookup

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
)

func serve(store supplier.LookupStore, principal *auth.Principal, method, target, body string) *httptest.ResponseRecorder {
	router := http.NewServeMux()
	NewHandlerWithClock(store, func() time.Time { return now }).RegisterRoutes(router)
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	if principal != nil {
		request = request.WithContext(auth.WithPrincipal(request.Context(), *principal))
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func decodeError(t *testing.T, response *httptest.ResponseRecorder) apperror.Error {
	t.Helper()
	require.Equal(t, "application/json", response.Header().Get("Content-Type"))
	var result apperror.Error
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
	return result
}

func TestListReturnsLiveItemsAndRetiredOnlyWhenRequested(t *testing.T) {
	store := newFakeStore()
	store.add(supplier.BuildingLookup, "Com 2", false)
	store.add(supplier.BuildingLookup, "Old Hall", true)

	response := serve(store, &reader, "GET", "/buildings", "")
	require.Equal(t, http.StatusOK, response.Code)
	require.JSONEq(t, `{"items":[{"id":"00000000-0000-4000-8000-000000000001","name":"Com 2","retired":false}]}`, response.Body.String())

	response = serve(store, &reader, "GET", "/buildings?includeRetired=true", "")
	require.Equal(t, http.StatusOK, response.Code)
	require.JSONEq(t, `{"items":[
		{"id":"00000000-0000-4000-8000-000000000001","name":"Com 2","retired":false},
		{"id":"00000000-0000-4000-8000-000000000002","name":"Old Hall","retired":true}
	]}`, response.Body.String())

	response = serve(store, &reader, "GET", "/buildings?includeRetired=false", "")
	require.Equal(t, http.StatusOK, response.Code)
	require.NotContains(t, response.Body.String(), "Old Hall")

	response = serve(store, &reader, "GET", "/supplier-types", "")
	require.Equal(t, http.StatusOK, response.Code)
	require.JSONEq(t, `{"items":[]}`, response.Body.String())
}

func TestListRejectsInvalidIncludeRetired(t *testing.T) {
	for _, value := range []string{"", "TRUE", "1", "yes"} {
		store := newFakeStore()
		response := serve(store, &reader, "GET", "/supplier-types?includeRetired="+value, "")
		require.Equal(t, http.StatusBadRequest, response.Code, value)
		result := decodeError(t, response)
		require.Equal(t, apperror.InvalidArgument, result.Code)
		require.Equal(t, []apperror.Field{{Field: "includeRetired", Message: "must be true or false"}}, result.Fields)
		require.Zero(t, store.calls)
	}
}

func TestCreateReturnsCreatedTrimmedRow(t *testing.T) {
	store := newFakeStore()
	response := serve(store, &admin, "POST", "/supplier-types", `{"name":"  Food & Drink "}`)
	require.Equal(t, http.StatusCreated, response.Code)
	require.JSONEq(t, `{"id":"00000000-0000-4000-8000-000000000001","name":"Food & Drink","retired":false}`, response.Body.String())
	require.Equal(t, "Food & Drink", store.rows[supplier.SupplierTypeLookup][0].Name)
}

func TestCreateRejectsInvalidBodiesWithFieldErrors(t *testing.T) {
	for _, test := range []struct {
		target, body string
		fields       []apperror.Field
	}{
		{"/buildings", ``, nil},
		{"/buildings", `not json`, nil},
		{"/buildings", `{"name":"Com 2","id":"00000000-0000-4000-8000-000000000001"}`, nil},
		{"/buildings", `{"name":"Com 2","retired":true}`, nil},
		{"/buildings", `{}`, []apperror.Field{{Field: "name", Message: "is required"}}},
		{"/buildings", `{"name":"` + strings.Repeat("b", 121) + `"}`, []apperror.Field{{Field: "name", Message: "is too long"}}},
		{"/supplier-types", `{"name":"` + strings.Repeat("t", 65) + `"}`, []apperror.Field{{Field: "name", Message: "is too long"}}},
		{"/supplier-types", `{"name":"&&"}`, []apperror.Field{{Field: "name", Message: "must contain a letter or digit"}}},
	} {
		store := newFakeStore()
		response := serve(store, &admin, "POST", test.target, test.body)
		require.Equal(t, http.StatusBadRequest, response.Code, test.body)
		result := decodeError(t, response)
		require.Equal(t, apperror.InvalidArgument, result.Code)
		require.Equal(t, test.fields, result.Fields, test.body)
		require.Zero(t, store.calls)
	}
}

func TestCreateConflictReturns409WithKindCode(t *testing.T) {
	store := newFakeStore()
	store.add(supplier.BuildingLookup, "Com 2", false)
	store.add(supplier.SupplierTypeLookup, "Food", false)

	response := serve(store, &admin, "POST", "/buildings", `{"name":"Com2"}`)
	require.Equal(t, http.StatusConflict, response.Code)
	result := decodeError(t, response)
	require.Equal(t, apperror.BuildingConflict, result.Code)
	require.Contains(t, result.Message, `building "Com 2"`)

	response = serve(store, &admin, "POST", "/supplier-types", `{"name":"FOOD"}`)
	require.Equal(t, http.StatusConflict, response.Code)
	require.Equal(t, apperror.SupplierTypeConflict, decodeError(t, response).Code)
}

func TestRetireResponses(t *testing.T) {
	store := newFakeStore()
	used := store.add(supplier.BuildingLookup, "Com 2", false)
	store.users[used.ID] = 2
	free := store.add(supplier.BuildingLookup, "Old Hall", false)
	typeRow := store.add(supplier.SupplierTypeLookup, "Food", false)
	store.users[typeRow.ID] = 1

	response := serve(store, &admin, "DELETE", "/buildings/"+string(used.ID), "")
	require.Equal(t, http.StatusConflict, response.Code)
	require.Equal(t, apperror.BuildingInUse, decodeError(t, response).Code)

	for range 2 {
		response = serve(store, &admin, "DELETE", "/buildings/"+string(free.ID), "")
		require.Equal(t, http.StatusNoContent, response.Code)
		require.Empty(t, response.Body.String())
	}

	response = serve(store, &admin, "DELETE", "/buildings/00000000-0000-4000-8000-000000000099", "")
	require.Equal(t, http.StatusNotFound, response.Code)
	require.Equal(t, apperror.BuildingNotFound, decodeError(t, response).Code)

	response = serve(store, &admin, "DELETE", "/supplier-types/"+string(typeRow.ID), "")
	require.Equal(t, http.StatusConflict, response.Code)
	require.Equal(t, apperror.SupplierTypeInUse, decodeError(t, response).Code)

	response = serve(store, &admin, "DELETE", "/supplier-types/00000000-0000-4000-8000-000000000099", "")
	require.Equal(t, http.StatusNotFound, response.Code)
	require.Equal(t, apperror.SupplierTypeNotFound, decodeError(t, response).Code)

	response = serve(store, &admin, "DELETE", "/supplier-types/not-a-uuid", "")
	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Equal(t, apperror.InvalidArgument, decodeError(t, response).Code)
}

func TestHandlersRequireTrustedPrincipalAndPermissions(t *testing.T) {
	retireTarget := "/buildings/00000000-0000-4000-8000-000000000001"
	for _, test := range []struct {
		name, method, target, body string
		principal                  *auth.Principal
		status                     int
		code                       apperror.Code
	}{
		{"anonymous list", "GET", "/buildings", "", nil, 401, apperror.Unauthenticated},
		{"anonymous create", "POST", "/supplier-types", `{"name":"Food"}`, nil, 401, apperror.Unauthenticated},
		{"anonymous retire", "DELETE", retireTarget, "", nil, 401, apperror.Unauthenticated},
		{"reader create", "POST", "/buildings", `{"name":"Com 2"}`, &reader, 403, apperror.Forbidden},
		{"reader retire", "DELETE", retireTarget, "", &reader, 403, apperror.Forbidden},
		// Permission is checked before the body is parsed.
		{"reader invalid body", "POST", "/buildings", `not json`, &reader, 403, apperror.Forbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newFakeStore()
			response := serve(store, test.principal, test.method, test.target+"?role=administrator", test.body)
			require.Equal(t, test.status, response.Code)
			require.Equal(t, test.code, decodeError(t, response).Code)
			require.Zero(t, store.calls)
		})
	}
}

func TestHandlersHideInternalFailures(t *testing.T) {
	for _, test := range []struct{ method, target, body string }{
		{"GET", "/buildings", ""},
		{"POST", "/supplier-types", `{"name":"Food"}`},
		{"DELETE", "/buildings/00000000-0000-4000-8000-000000000001", ""},
	} {
		store := newFakeStore()
		store.err = errors.New("database password is secret")
		response := serve(store, &admin, test.method, test.target, test.body)
		require.Equal(t, http.StatusInternalServerError, response.Code)
		require.Equal(t, apperror.Internal, decodeError(t, response).Code)
		require.NotContains(t, response.Body.String(), "secret")
	}
	store := newFakeStore()
	store.err = &apperror.Error{Code: apperror.DependencyUnavailable, Message: "database password is secret"}
	response := serve(store, &reader, "GET", "/buildings", "")
	require.Equal(t, http.StatusServiceUnavailable, response.Code)
	require.NotContains(t, response.Body.String(), "secret")
}

func TestNoRenameRoute(t *testing.T) {
	response := serve(newFakeStore(), &admin, "PATCH", "/buildings/00000000-0000-4000-8000-000000000001", `{"name":"New"}`)
	require.Equal(t, http.StatusMethodNotAllowed, response.Code)
}
