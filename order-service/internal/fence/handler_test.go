package fence

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/auth/authtest"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/httpapi"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/serviceauth"
)

const (
	testOperationID = "6f1c2b9e-0d4a-4c3b-9a8e-2f7d1e5c4b3a"
	testSupplierID  = "0b8e6a52-3c1d-4f7e-8a9b-1c2d3e4f5a6b"
	fixtureClient   = "fixture-client@clients"
)

type call struct {
	method, actor, operationID, supplierID string
}

type fakeFences struct {
	calls  []call
	result Fence
	err    error
}

func (f *fakeFences) Acquire(_ context.Context, actor, operationID, supplierID string) (Fence, error) {
	f.calls = append(f.calls, call{"acquire", actor, operationID, supplierID})
	return f.result, f.err
}

func (f *fakeFences) Inspect(_ context.Context, operationID string) (Fence, error) {
	f.calls = append(f.calls, call{"inspect", "", operationID, ""})
	return f.result, f.err
}

func (f *fakeFences) Commit(_ context.Context, actor, operationID string) (Fence, error) {
	f.calls = append(f.calls, call{"commit", actor, operationID, ""})
	return f.result, f.err
}

func (f *fakeFences) Release(_ context.Context, actor, operationID string) (Fence, error) {
	f.calls = append(f.calls, call{"release", actor, operationID, ""})
	return f.result, f.err
}

type harness struct {
	router  http.Handler
	fixture *authtest.Fixture
	service string // a valid service token with orders:internal
}

func newHarness(t *testing.T, fences Fences) harness {
	t.Helper()
	f := authtest.New(t)
	cfg := auth.DefaultAuth0Config()
	cfg.Issuer = f.Server.URL + "/"
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	verifier, err := auth.NewAuth0(cfg, logger, auth.WithHTTPClient(f.Server.Client()))
	require.NoError(t, err)
	guard := serviceauth.NewMiddleware(auth.NewMiddleware(verifier))
	router := httpapi.NewRouter(httpapi.Dependencies{Logger: logger}, NewHandler(fences, guard, logger))
	token := f.Token(t, f.ServiceClaims(auth.OrderAudience, []string{string(auth.InternalAccess)}), "one")
	return harness{router: router, fixture: f, service: token}
}

func (h harness) do(t *testing.T, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	h.router.ServeHTTP(recorder, request)
	return recorder
}

func fencePath(operationID, suffix string) string {
	path := "/internal/supplier-fences/" + operationID
	if suffix != "" {
		path += "/" + suffix
	}
	return path
}

func decodeError(t *testing.T, recorder *httptest.ResponseRecorder) apperror.Error {
	t.Helper()
	require.Equal(t, "application/json", recorder.Header().Get("Content-Type"))
	var body apperror.Error
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	return body
}

func TestRoutesReturnFenceBody_C2(t *testing.T) {
	fences := &fakeFences{result: Fence{OperationID: testOperationID, SupplierID: testSupplierID, State: StateOpen, HasActiveErrands: true}}
	h := newHarness(t, fences)

	routes := []struct {
		method, suffix, body string
		want                 call
	}{
		{http.MethodPost, "acquire", `{"supplierId":"` + testSupplierID + `"}`, call{"acquire", fixtureClient, testOperationID, testSupplierID}},
		{http.MethodGet, "", "", call{"inspect", "", testOperationID, ""}},
		{http.MethodPost, "commit", "", call{"commit", fixtureClient, testOperationID, ""}},
		{http.MethodPost, "release", "", call{"release", fixtureClient, testOperationID, ""}},
	}
	for _, route := range routes {
		t.Run(route.want.method, func(t *testing.T) {
			fences.calls = nil
			recorder := h.do(t, route.method, fencePath(testOperationID, route.suffix), h.service, route.body)

			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			require.Equal(t, "application/json", recorder.Header().Get("Content-Type"))
			require.JSONEq(t, `{"operationId":"`+testOperationID+`","supplierId":"`+testSupplierID+`","state":"open","hasActiveErrands":true}`, recorder.Body.String())
			require.Equal(t, []call{route.want}, fences.calls)
		})
	}
}

func TestRoutesRequireServiceCredential_NFR3_5_3(t *testing.T) {
	fences := &fakeFences{}
	h := newHarness(t, fences)
	f := h.fixture
	internal := []string{string(auth.InternalAccess)}
	userToken := f.Token(t, f.Claims(auth.OrderAudience, internal), "one")
	noPermission := f.Token(t, f.ServiceClaims(auth.OrderAudience, []string{string(auth.UseErrands)}), "one")

	tokens := []struct {
		name   string
		token  string
		status int
		code   apperror.Code
	}{
		{"no token", "", http.StatusUnauthorized, apperror.Unauthenticated},
		{"invalid token", "not.a.jwt", http.StatusUnauthorized, apperror.Unauthenticated},
		{"user token with internal permission", userToken, http.StatusForbidden, apperror.Forbidden},
		{"service token without internal permission", noPermission, http.StatusForbidden, apperror.Forbidden},
	}
	routes := []struct{ method, suffix, body string }{
		{http.MethodPost, "acquire", `{"supplierId":"` + testSupplierID + `"}`},
		{http.MethodGet, "", ""},
		{http.MethodPost, "commit", ""},
		{http.MethodPost, "release", ""},
	}
	for _, tt := range tokens {
		for _, route := range routes {
			t.Run(tt.name+" "+route.method+" "+route.suffix, func(t *testing.T) {
				recorder := h.do(t, route.method, fencePath(testOperationID, route.suffix), tt.token, route.body)
				require.Equal(t, tt.status, recorder.Code)
				require.Equal(t, tt.code, decodeError(t, recorder).Code)
			})
		}
	}
	require.Empty(t, fences.calls, "a rejected credential never reaches the fence")
}

func TestRejectsMalformedRequests_NFR2_2_1(t *testing.T) {
	fences := &fakeFences{}
	h := newHarness(t, fences)

	tests := []struct {
		name   string
		method string
		path   string
		body   string
		code   apperror.Code
		fields []apperror.Field
	}{
		{name: "inspect bad operation ID", method: http.MethodGet, path: fencePath("not-a-uuid", ""), code: apperror.InvalidArgument},
		{name: "commit bad operation ID", method: http.MethodPost, path: fencePath("123", "commit"), code: apperror.InvalidArgument},
		{name: "release bad operation ID", method: http.MethodPost, path: fencePath("123", "release"), code: apperror.InvalidArgument},
		{name: "acquire bad operation ID", method: http.MethodPost, path: fencePath("123", "acquire"), body: `{"supplierId":"` + testSupplierID + `"}`, code: apperror.InvalidArgument},
		{name: "acquire without body", method: http.MethodPost, path: fencePath(testOperationID, "acquire"), code: apperror.InvalidArgument},
		{name: "acquire with malformed JSON", method: http.MethodPost, path: fencePath(testOperationID, "acquire"), body: `{"supplierId":`, code: apperror.InvalidArgument},
		{name: "acquire with trailing data", method: http.MethodPost, path: fencePath(testOperationID, "acquire"), body: `{"supplierId":"` + testSupplierID + `"} {}`, code: apperror.InvalidArgument},
		{name: "acquire without supplier", method: http.MethodPost, path: fencePath(testOperationID, "acquire"), body: `{}`, code: apperror.ValidationFailed,
			fields: []apperror.Field{{Field: "supplierId", Message: "is required"}}},
		{name: "acquire with null supplier", method: http.MethodPost, path: fencePath(testOperationID, "acquire"), body: `{"supplierId":null}`, code: apperror.ValidationFailed,
			fields: []apperror.Field{{Field: "supplierId", Message: "is required"}}},
		{name: "acquire with non-UUID supplier", method: http.MethodPost, path: fencePath(testOperationID, "acquire"), body: `{"supplierId":"abc"}`, code: apperror.ValidationFailed,
			fields: []apperror.Field{{Field: "supplierId", Message: "must be a UUID"}}},
		{name: "acquire with numeric supplier", method: http.MethodPost, path: fencePath(testOperationID, "acquire"), body: `{"supplierId":42}`, code: apperror.ValidationFailed,
			fields: []apperror.Field{{Field: "supplierId", Message: "must be a UUID string"}}},
		{name: "acquire with unknown field", method: http.MethodPost, path: fencePath(testOperationID, "acquire"), body: `{"supplierId":"` + testSupplierID + `","force":true}`, code: apperror.ValidationFailed,
			fields: []apperror.Field{{Field: "force", Message: "is not allowed"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := h.do(t, tt.method, tt.path, h.service, tt.body)
			require.Equal(t, http.StatusBadRequest, recorder.Code)
			body := decodeError(t, recorder)
			require.Equal(t, tt.code, body.Code)
			require.Equal(t, tt.fields, body.Fields)
		})
	}
	require.Empty(t, fences.calls)
}

func TestNormalisesIDsToLowerCase(t *testing.T) {
	fences := &fakeFences{result: Fence{State: StateOpen}}
	h := newHarness(t, fences)

	recorder := h.do(t, http.MethodPost, fencePath(strings.ToUpper(testOperationID), "acquire"), h.service,
		`{"supplierId":"`+strings.ToUpper(testSupplierID)+`"}`)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, []call{{"acquire", fixtureClient, testOperationID, testSupplierID}}, fences.calls)
}

func TestMapsFenceErrors_C2(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   apperror.Code
	}{
		{"supplier mismatch", &apperror.Error{Code: apperror.InvalidArgument, Message: "operationId is already used for another supplier"}, http.StatusBadRequest, apperror.InvalidArgument},
		{"not found", &apperror.Error{Code: apperror.FenceNotFound, Message: "fence not found"}, http.StatusNotFound, apperror.FenceNotFound},
		{"terminal conflict", &apperror.Error{Code: apperror.InvalidTransition, Message: "conflict"}, http.StatusConflict, apperror.InvalidTransition},
		{"internal failure", errors.New("connection refused to postgres://secret@db"), http.StatusInternalServerError, apperror.Internal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, &fakeFences{err: tt.err})
			recorder := h.do(t, http.MethodPost, fencePath(testOperationID, "commit"), h.service, "")

			require.Equal(t, tt.status, recorder.Code)
			body := decodeError(t, recorder)
			require.Equal(t, tt.code, body.Code)
			require.NotContains(t, recorder.Body.String(), "secret", "internal errors are never echoed")
		})
	}
}
