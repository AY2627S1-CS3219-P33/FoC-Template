package update

import (
	"bytes"
	"context"
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

type fakeWriter struct {
	updatedSupplier supplier.Supplier
	err             error
	gotID           supplier.SupplierID
	gotPatch        supplier.Patch
	gotTime         time.Time
	calls           int
}

func (f *fakeWriter) Create(_ context.Context, _ supplier.Details, _ time.Time) (supplier.Supplier, error) {
	panic("unexpected Create call")
}

func (f *fakeWriter) Update(_ context.Context, id supplier.SupplierID, patch supplier.Patch, now time.Time) (supplier.Supplier, error) {
	f.calls++
	f.gotID = id
	f.gotPatch = patch
	f.gotTime = now
	if f.err != nil {
		return supplier.Supplier{}, f.err
	}
	return f.updatedSupplier, nil
}

var (
	testTime       = time.Date(2026, time.October, 1, 3, 0, 0, 0, time.UTC)
	testSupplierID = supplier.SupplierID("29e9aa8b-5651-4981-8828-3fbb1b21fcb1")
	testOldVerID   = supplier.VersionID("173fd7be-4f54-4f15-a8b1-58bd502344a0")
	testNewVerID   = supplier.VersionID("b92db9bb-ff5a-4b95-a228-4efc381c8558")
)

func serveUpdate(t *testing.T, writer supplier.Writer, supplierID string, payload any, principal *auth.Principal, clock func() time.Time) *httptest.ResponseRecorder {
	t.Helper()
	router := http.NewServeMux()
	if clock == nil {
		clock = func() time.Time { return testTime }
	}
	NewHandlerWithClock(writer, clock).RegisterRoutes(router)

	var body []byte
	if payload != nil {
		switch v := payload.(type) {
		case string:
			body = []byte(v)
		case []byte:
			body = v
		default:
			var err error
			body, err = json.Marshal(payload)
			require.NoError(t, err)
		}
	}

	var req *http.Request
	path := "/suppliers/" + supplierID
	if payload == nil {
		req = httptest.NewRequest(http.MethodPatch, path, nil)
	} else {
		req = httptest.NewRequest(http.MethodPatch, path, bytes.NewReader(body))
	}
	req.Header.Set("Content-Type", "application/json")

	if principal != nil {
		req = req.WithContext(auth.WithPrincipal(req.Context(), *principal))
	}

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	return recorder
}

func adminPrincipal() *auth.Principal {
	return &auth.Principal{
		Subject: "admin-user-1",
		Roles:   []auth.Role{auth.RoleAdministrator},
	}
}

func normalPrincipal() *auth.Principal {
	return &auth.Principal{
		Subject: "normal-user-1",
		Roles:   []auth.Role{auth.RoleUser},
	}
}

func TestAdminCanUpdateSupplierSuccessfully(t *testing.T) {
	img := "https://example.com/new-pic.png"
	writer := &fakeWriter{
		updatedSupplier: supplier.Supplier{
			SupplierID: testSupplierID,
			VersionID:  testNewVerID,
			Details: supplier.Details{
				Name:                "The Updated Deck",
				Type:                "food",
				Building:            "Arts Link",
				Floor:               "2",
				LocationDescription: "Beside the central library",
				Latitude:            1.294,
				Longitude:           103.772,
				OpeningTime:         "07:30",
				ClosingTime:         "21:30",
				ImageURL:            &img,
			},
			Available: true,
			CreatedAt: testTime.Add(-time.Hour),
			UpdatedAt: testTime,
		},
	}

	payload := map[string]any{
		"name":        "The Updated Deck",
		"openingTime": "07:30",
		"closingTime": "21:30",
		"imageUrl":    img,
	}

	resp := serveUpdate(t, writer, string(testSupplierID), payload, adminPrincipal(), nil)

	require.Equal(t, http.StatusOK, resp.Code)
	require.Equal(t, 1, writer.calls)
	require.Equal(t, testSupplierID, writer.gotID)
	require.Equal(t, testTime, writer.gotTime)
	require.NotNil(t, writer.gotPatch.Name)
	require.Equal(t, "The Updated Deck", *writer.gotPatch.Name)
	require.NotNil(t, writer.gotPatch.OpeningTime)
	require.Equal(t, "07:30", *writer.gotPatch.OpeningTime)
	require.NotNil(t, writer.gotPatch.ClosingTime)
	require.Equal(t, "21:30", *writer.gotPatch.ClosingTime)
	require.True(t, writer.gotPatch.ImageURLSet)
	require.Equal(t, &img, writer.gotPatch.ImageURL)

	require.JSONEq(t, `{
		"supplierId": "29e9aa8b-5651-4981-8828-3fbb1b21fcb1",
		"versionId": "b92db9bb-ff5a-4b95-a228-4efc381c8558",
		"name": "The Updated Deck",
		"type": "food",
		"building": "Arts Link",
		"floor": "2",
		"locationDescription": "Beside the central library",
		"latitude": 1.294,
		"longitude": 103.772,
		"openingTime": "07:30",
		"closingTime": "21:30",
		"imageUrl": "https://example.com/new-pic.png",
		"available": true,
		"createdAt": "2026-10-01T02:00:00Z",
		"updatedAt": "2026-10-01T03:00:00Z"
	}`, resp.Body.String())
}

func TestAdminCanSetImageURLToNull(t *testing.T) {
	writer := &fakeWriter{
		updatedSupplier: supplier.Supplier{
			SupplierID: testSupplierID,
			VersionID:  testNewVerID,
			Details: supplier.Details{
				Name:                "The Deck",
				Type:                "food",
				Building:            "Arts Link",
				Floor:               "2",
				LocationDescription: "Beside the central library",
				Latitude:            1.294,
				Longitude:           103.772,
				OpeningTime:         "08:00",
				ClosingTime:         "20:00",
				ImageURL:            nil,
			},
			Available: true,
			CreatedAt: testTime.Add(-time.Hour),
			UpdatedAt: testTime,
		},
	}

	payload := `{"imageUrl": null}`
	resp := serveUpdate(t, writer, string(testSupplierID), payload, adminPrincipal(), nil)

	require.Equal(t, http.StatusOK, resp.Code)
	require.Equal(t, 1, writer.calls)
	require.True(t, writer.gotPatch.ImageURLSet)
	require.Nil(t, writer.gotPatch.ImageURL)
}

func TestAdminCanUpdateSingleField(t *testing.T) {
	writer := &fakeWriter{
		updatedSupplier: supplier.Supplier{
			SupplierID: testSupplierID,
			VersionID:  testNewVerID,
			Details: supplier.Details{
				Name:                "The Deck",
				Type:                "food",
				Building:            "University Town",
				Floor:               "2",
				LocationDescription: "Beside the central library",
				Latitude:            1.294,
				Longitude:           103.772,
				OpeningTime:         "08:00",
				ClosingTime:         "20:00",
			},
			Available: true,
			CreatedAt: testTime,
			UpdatedAt: testTime,
		},
	}

	payload := map[string]any{"building": "University Town"}
	resp := serveUpdate(t, writer, string(testSupplierID), payload, adminPrincipal(), nil)

	require.Equal(t, http.StatusOK, resp.Code)
	require.Equal(t, 1, writer.calls)
	require.NotNil(t, writer.gotPatch.Building)
	require.Equal(t, "University Town", *writer.gotPatch.Building)
	require.Nil(t, writer.gotPatch.Name)
	require.False(t, writer.gotPatch.ImageURLSet)
}

func TestUpdateSupplierRequiresAuthentication(t *testing.T) {
	writer := &fakeWriter{}

	resp := serveUpdate(t, writer, string(testSupplierID), map[string]any{"name": "New"}, nil, nil)

	require.Equal(t, http.StatusUnauthorized, resp.Code)
	require.Equal(t, 0, writer.calls)
	require.JSONEq(t, `{"code":"UNAUTHENTICATED","message":"authentication is required"}`, resp.Body.String())
}

func TestUpdateSupplierRequiresManagePermission(t *testing.T) {
	writer := &fakeWriter{}

	resp := serveUpdate(t, writer, string(testSupplierID), map[string]any{"name": "New"}, normalPrincipal(), nil)

	require.Equal(t, http.StatusForbidden, resp.Code)
	require.Equal(t, 0, writer.calls)
	require.JSONEq(t, `{"code":"FORBIDDEN","message":"insufficient permissions for this operation"}`, resp.Body.String())
}

func TestUpdateSupplierRejectsInvalidSupplierIDInPath(t *testing.T) {
	writer := &fakeWriter{}

	resp := serveUpdate(t, writer, "not-a-valid-uuid", map[string]any{"name": "New"}, adminPrincipal(), nil)

	require.Equal(t, http.StatusBadRequest, resp.Code)
	require.Equal(t, 0, writer.calls)
	require.JSONEq(t, `{"code":"INVALID_ARGUMENT","message":"supplierId must be a UUID"}`, resp.Body.String())
}

func TestUpdateSupplierRejectsEmptyBody(t *testing.T) {
	writer := &fakeWriter{}

	resp := serveUpdate(t, writer, string(testSupplierID), "", adminPrincipal(), nil)

	require.Equal(t, http.StatusBadRequest, resp.Code)
	require.Equal(t, 0, writer.calls)
	var errResp apperror.Error
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&errResp))
	require.Equal(t, apperror.InvalidArgument, errResp.Code)
	require.Equal(t, "request body is required", errResp.Message)
}

func TestUpdateSupplierRejectsMalformedJSON(t *testing.T) {
	writer := &fakeWriter{}

	resp := serveUpdate(t, writer, string(testSupplierID), `{"name": "unclosed`, adminPrincipal(), nil)

	require.Equal(t, http.StatusBadRequest, resp.Code)
	require.Equal(t, 0, writer.calls)
	var errResp apperror.Error
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&errResp))
	require.Equal(t, apperror.InvalidArgument, errResp.Code)
	require.Contains(t, errResp.Message, "request body must be valid JSON")
}

func TestUpdateSupplierRejectsUnknownFields(t *testing.T) {
	writer := &fakeWriter{}

	resp := serveUpdate(t, writer, string(testSupplierID), `{"name": "Valid", "unexpected": "value"}`, adminPrincipal(), nil)

	require.Equal(t, http.StatusBadRequest, resp.Code)
	require.Equal(t, 0, writer.calls)
	var errResp apperror.Error
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&errResp))
	require.Equal(t, apperror.InvalidArgument, errResp.Code)
	require.Contains(t, errResp.Message, "unknown field")
}

func TestUpdateSupplierRejectsClientSuppliedSupplierID(t *testing.T) {
	writer := &fakeWriter{}
	payload := map[string]any{
		"supplierId": "29e9aa8b-5651-4981-8828-3fbb1b21fcb1",
		"name":       "New Name",
	}

	resp := serveUpdate(t, writer, string(testSupplierID), payload, adminPrincipal(), nil)

	require.Equal(t, http.StatusBadRequest, resp.Code)
	require.Equal(t, 0, writer.calls)
	var errResp apperror.Error
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&errResp))
	require.Equal(t, apperror.InvalidArgument, errResp.Code)
	require.Equal(t, "supplier fields are invalid", errResp.Message)
	require.Contains(t, errResp.Fields, apperror.Field{
		Field:   "supplierId",
		Message: "must not be provided",
	})
}

func TestUpdateSupplierRejectsClientSuppliedVersionID(t *testing.T) {
	writer := &fakeWriter{}
	payload := map[string]any{
		"versionId": "173fd7be-4f54-4f15-a8b1-58bd502344a0",
		"name":      "New Name",
	}

	resp := serveUpdate(t, writer, string(testSupplierID), payload, adminPrincipal(), nil)

	require.Equal(t, http.StatusBadRequest, resp.Code)
	require.Equal(t, 0, writer.calls)
	var errResp apperror.Error
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&errResp))
	require.Equal(t, apperror.InvalidArgument, errResp.Code)
	require.Equal(t, "supplier fields are invalid", errResp.Message)
	require.Contains(t, errResp.Fields, apperror.Field{
		Field:   "versionId",
		Message: "must not be provided",
	})
}

func TestUpdateSupplierRejectsEmptyPatch(t *testing.T) {
	writer := &fakeWriter{}

	resp := serveUpdate(t, writer, string(testSupplierID), `{}`, adminPrincipal(), nil)

	require.Equal(t, http.StatusBadRequest, resp.Code)
	require.Equal(t, 0, writer.calls)
	var errResp apperror.Error
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&errResp))
	require.Equal(t, apperror.InvalidArgument, errResp.Code)
	require.Equal(t, "patch must contain at least one field", errResp.Message)
}

func TestUpdateSupplierRejectsInvalidFieldValues(t *testing.T) {
	writer := &fakeWriter{}
	tooLong := strings.Repeat("x", 121)
	payload := map[string]any{
		"name":        "   ",
		"building":    tooLong,
		"latitude":    91.0,
		"longitude":   -181.0,
		"openingTime": "invalid-time",
		"closingTime": "24:00",
		"imageUrl":    "not-a-url",
	}

	resp := serveUpdate(t, writer, string(testSupplierID), payload, adminPrincipal(), nil)

	require.Equal(t, http.StatusBadRequest, resp.Code)
	require.Equal(t, 0, writer.calls)

	var errResp apperror.Error
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&errResp))
	require.Equal(t, apperror.InvalidArgument, errResp.Code)
	require.Equal(t, "supplier fields are invalid", errResp.Message)

	fieldNames := make([]string, len(errResp.Fields))
	for i, f := range errResp.Fields {
		fieldNames[i] = f.Field
	}
	require.Contains(t, fieldNames, "name")
	require.Contains(t, fieldNames, "building")
	require.Contains(t, fieldNames, "latitude")
	require.Contains(t, fieldNames, "longitude")
	require.Contains(t, fieldNames, "openingTime")
	require.Contains(t, fieldNames, "closingTime")
	require.Contains(t, fieldNames, "imageUrl")
}

func TestUpdateSupplierRejectsIdenticalOpeningAndClosingTimeInPatch(t *testing.T) {
	writer := &fakeWriter{}
	payload := map[string]any{
		"openingTime": "09:00",
		"closingTime": "09:00",
	}

	resp := serveUpdate(t, writer, string(testSupplierID), payload, adminPrincipal(), nil)

	require.Equal(t, http.StatusBadRequest, resp.Code)
	require.Equal(t, 0, writer.calls)

	var errResp apperror.Error
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&errResp))
	require.Equal(t, apperror.InvalidArgument, errResp.Code)
	require.Contains(t, errResp.Fields, apperror.Field{
		Field:   "closingTime",
		Message: "must differ from openingTime",
	})
}

func TestUpdateSupplierReturnsNotFoundWhenSupplierMissing(t *testing.T) {
	writer := &fakeWriter{
		err: &apperror.Error{Code: apperror.SupplierNotFound, Message: "supplier not found"},
	}

	resp := serveUpdate(t, writer, string(testSupplierID), map[string]any{"name": "New"}, adminPrincipal(), nil)

	require.Equal(t, http.StatusNotFound, resp.Code)
	require.Equal(t, 1, writer.calls)
	require.JSONEq(t, `{"code":"SUPPLIER_NOT_FOUND","message":"supplier not found"}`, resp.Body.String())
}

func TestUpdateSupplierMapsNormalizedNameConflict(t *testing.T) {
	writer := &fakeWriter{
		err: apperror.ErrSupplierNameConflict,
	}

	resp := serveUpdate(t, writer, string(testSupplierID), map[string]any{"name": "Conflicting Name"}, adminPrincipal(), nil)

	require.Equal(t, http.StatusConflict, resp.Code)
	require.Equal(t, 1, writer.calls)
	require.JSONEq(t, `{"code":"SUPPLIER_NAME_CONFLICT","message":"a supplier with this normalized name already exists"}`, resp.Body.String())
}

func TestUpdateSupplierMapsValidationErrorFromRepository(t *testing.T) {
	writer := &fakeWriter{
		err: &supplier.ValidationError{
			Violations: []supplier.FieldViolation{
				{Field: "closingTime", Message: "must differ from openingTime"},
			},
		},
	}

	resp := serveUpdate(t, writer, string(testSupplierID), map[string]any{"openingTime": "18:00"}, adminPrincipal(), nil)

	require.Equal(t, http.StatusBadRequest, resp.Code)
	require.Equal(t, 1, writer.calls)

	var errResp apperror.Error
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&errResp))
	require.Equal(t, apperror.InvalidArgument, errResp.Code)
	require.Equal(t, "supplier fields are invalid", errResp.Message)
	require.Contains(t, errResp.Fields, apperror.Field{
		Field:   "closingTime",
		Message: "must differ from openingTime",
	})
}

func TestUpdateSupplierMapsUnexpectedRepositoryError(t *testing.T) {
	writer := &fakeWriter{
		err: errors.New("database connection refused: password=secret"),
	}

	resp := serveUpdate(t, writer, string(testSupplierID), map[string]any{"name": "New"}, adminPrincipal(), nil)

	require.Equal(t, http.StatusInternalServerError, resp.Code)
	require.Equal(t, 1, writer.calls)
	require.JSONEq(t, `{"code":"INTERNAL","message":"an unexpected internal error occurred"}`, resp.Body.String())
	require.NotContains(t, resp.Body.String(), "secret")
}

func TestNewHandlerPanicsOnNilWriter(t *testing.T) {
	require.Panics(t, func() {
		NewHandler(nil)
	})
}

func TestNewHandlerWithClockPanicsOnNilArguments(t *testing.T) {
	writer := &fakeWriter{}
	require.Panics(t, func() {
		NewHandlerWithClock(nil, func() time.Time { return testTime })
	})
	require.Panics(t, func() {
		NewHandlerWithClock(writer, nil)
	})
}
