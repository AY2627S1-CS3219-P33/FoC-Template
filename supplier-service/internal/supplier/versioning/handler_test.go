package versioning

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
)

const testVersionID = supplier.VersionID("173fd7be-4f54-4f15-a8b1-58bd502344a0")

type readerStub struct {
	version supplier.Version
	err     error
	gotID   supplier.VersionID
	calls   int
}

func (r *readerStub) ListAvailable(context.Context, supplier.ListFilter) (supplier.Page, error) {
	panic("unexpected ListAvailable call")
}

func (r *readerStub) GetCurrentAvailable(context.Context, supplier.SupplierID) (supplier.Supplier, error) {
	panic("unexpected GetCurrentAvailable call")
}

func (r *readerStub) GetVersion(_ context.Context, versionID supplier.VersionID) (supplier.Version, error) {
	r.calls++
	r.gotID = versionID
	return r.version, r.err
}

func TestAuthenticatedUserCanReadImmutableVersion(t *testing.T) {
	createdAt := time.Date(2026, time.September, 30, 8, 15, 0, 0, time.UTC)
	imageURL := "https://example.com/supplier.png"
	reader := &readerStub{version: supplier.Version{
		SupplierID: "29e9aa8b-5651-4981-8828-3fbb1b21fcb1",
		VersionID:  testVersionID,
		Details: supplier.Details{
			Name:                "The Deck",
			Type:                "food",
			Building:            "Arts Link",
			Floor:               "2",
			LocationDescription: "Beside the library",
			Latitude:            1.294,
			Longitude:           103.772,
			OpeningTime:         "08:00",
			ClosingTime:         "20:00",
			ImageURL:            &imageURL,
		},
		Available: false,
		CreatedAt: createdAt,
	}}

	response := serveVersion(t, reader, testVersionID, true)

	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, testVersionID, reader.gotID)
	require.JSONEq(t, `{
		"supplierId":"29e9aa8b-5651-4981-8828-3fbb1b21fcb1",
		"versionId":"173fd7be-4f54-4f15-a8b1-58bd502344a0",
		"name":"The Deck",
		"type":"food",
		"building":"Arts Link",
		"floor":"2",
		"locationDescription":"Beside the library",
		"latitude":1.294,
		"longitude":103.772,
		"openingTime":"08:00",
		"closingTime":"20:00",
		"imageUrl":"https://example.com/supplier.png",
		"available":false,
		"createdAt":"2026-09-30T08:15:00Z"
	}`, response.Body.String())
}

func TestVersionLookupRequiresAuthentication(t *testing.T) {
	reader := &readerStub{}

	response := serveVersion(t, reader, testVersionID, false)

	require.Equal(t, http.StatusUnauthorized, response.Code)
	require.Zero(t, reader.calls)
	require.JSONEq(t, `{"code":"UNAUTHENTICATED","message":"authentication is required"}`, response.Body.String())
}

func TestVersionLookupRejectsInvalidIDBeforeRepositoryCall(t *testing.T) {
	reader := &readerStub{}

	response := serveVersion(t, reader, "not-a-uuid", true)

	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Zero(t, reader.calls)
	require.JSONEq(t, `{"code":"INVALID_ARGUMENT","message":"versionId must be a UUID"}`, response.Body.String())
}

func TestVersionLookupMapsNotFound(t *testing.T) {
	reader := &readerStub{err: &apperror.Error{
		Code:    apperror.SupplierVersionNotFound,
		Message: "supplier version not found",
	}}

	response := serveVersion(t, reader, testVersionID, true)

	require.Equal(t, http.StatusNotFound, response.Code)
	require.JSONEq(t, `{"code":"SUPPLIER_VERSION_NOT_FOUND","message":"supplier version not found"}`, response.Body.String())
}

func TestVersionLookupDoesNotExposeRepositoryErrors(t *testing.T) {
	reader := &readerStub{err: errors.New("database password is secret")}

	response := serveVersion(t, reader, testVersionID, true)

	require.Equal(t, http.StatusInternalServerError, response.Code)
	require.NotContains(t, response.Body.String(), "database password")
	require.JSONEq(t, `{"code":"INTERNAL","message":"an unexpected internal error occurred"}`, response.Body.String())
}

func TestVersionLookupMapsDependencyUnavailableWithoutLeakingDetails(t *testing.T) {
	reader := &readerStub{err: &apperror.Error{Code: apperror.DependencyUnavailable, Message: "database password is secret"}}
	response := serveVersion(t, reader, testVersionID, true)
	require.Equal(t, http.StatusServiceUnavailable, response.Code)
	require.JSONEq(t, `{"code":"DEPENDENCY_UNAVAILABLE","message":"a required dependency is unavailable"}`, response.Body.String())
}

func serveVersion(t *testing.T, reader supplier.Reader, versionID supplier.VersionID, authenticated bool) *httptest.ResponseRecorder {
	t.Helper()
	router := http.NewServeMux()
	NewHandler(reader).RegisterRoutes(router)
	request := httptest.NewRequest(http.MethodGet, "/supplier-versions/"+string(versionID), nil)
	if authenticated {
		request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{Subject: "account-1", Permissions: []auth.Permission{auth.ReadSuppliers}}))
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}
