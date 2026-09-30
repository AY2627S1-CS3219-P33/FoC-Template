package create

import (
	"bytes"
	"context"
	"encoding/json"
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

type fakeWriter struct {
	createdSupplier supplier.Supplier
	err             error
	gotDetails      supplier.Details
	gotTime         time.Time
	calls           int
}

func (f *fakeWriter) Create(_ context.Context, details supplier.Details, now time.Time) (supplier.Supplier, error) {
	f.calls++
	f.gotDetails = details
	f.gotTime = now
	if f.err != nil {
		return supplier.Supplier{}, f.err
	}
	return f.createdSupplier, nil
}

func (f *fakeWriter) Update(context.Context, supplier.SupplierID, supplier.Patch, time.Time) (supplier.Supplier, error) {
	panic("unexpected Update call")
}

var (
	testTime      = time.Date(2026, time.October, 1, 3, 0, 0, 0, time.UTC)
	testSupplierID = supplier.SupplierID("29e9aa8b-5651-4981-8828-3fbb1b21fcb1")
	testVersionID  = supplier.VersionID("173fd7be-4f54-4f15-a8b1-58bd502344a0")
)

func validPayload() map[string]any {
	return map[string]any{
		"name":                "The Deck",
		"type":                "food",
		"building":            "Arts Link",
		"floor":               "2",
		"locationDescription": "Beside the central library",
		"latitude":            1.294,
		"longitude":           103.772,
		"openingTime":         "08:00",
		"closingTime":         "20:00",
	}
}

func serveCreate(t *testing.T, writer supplier.Writer, payload any, principal *auth.Principal, clock func() time.Time) *httptest.ResponseRecorder {
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
	if payload == nil {
		req = httptest.NewRequest(http.MethodPost, "/suppliers", nil)
	} else {
		req = httptest.NewRequest(http.MethodPost, "/suppliers", bytes.NewReader(body))
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

func TestAdminCanCreateSupplierSuccessfully(t *testing.T) {
	writer := &fakeWriter{
		createdSupplier: supplier.Supplier{
			SupplierID: testSupplierID,
			VersionID:  testVersionID,
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
			},
			Available: true,
			CreatedAt: testTime,
			UpdatedAt: testTime,
		},
	}

	resp := serveCreate(t, writer, validPayload(), adminPrincipal(), nil)

	require.Equal(t, http.StatusCreated, resp.Code)
	require.Equal(t, "/suppliers/29e9aa8b-5651-4981-8828-3fbb1b21fcb1", resp.Header().Get("Location"))
	require.Equal(t, 1, writer.calls)
	require.Equal(t, testTime, writer.gotTime)
	require.Equal(t, "The Deck", writer.gotDetails.Name)
	require.Equal(t, 1.294, writer.gotDetails.Latitude)
	require.Equal(t, 103.772, writer.gotDetails.Longitude)

	require.JSONEq(t, `{
		"supplierId": "29e9aa8b-5651-4981-8828-3fbb1b21fcb1",
		"versionId": "173fd7be-4f54-4f15-a8b1-58bd502344a0",
		"name": "The Deck",
		"type": "food",
		"building": "Arts Link",
		"floor": "2",
		"locationDescription": "Beside the central library",
		"latitude": 1.294,
		"longitude": 103.772,
		"openingTime": "08:00",
		"closingTime": "20:00",
		"available": true,
		"createdAt": "2026-10-01T03:00:00Z",
		"updatedAt": "2026-10-01T03:00:00Z"
	}`, resp.Body.String())
}

func TestAdminCanCreateSupplierWithOptionalImageURL(t *testing.T) {
	imgURL := "https://example.com/supplier.png"
	payload := validPayload()
	payload["imageUrl"] = imgURL

	writer := &fakeWriter{
		createdSupplier: supplier.Supplier{
			SupplierID: testSupplierID,
			VersionID:  testVersionID,
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
				ImageURL:            &imgURL,
			},
			Available: true,
			CreatedAt: testTime,
			UpdatedAt: testTime,
		},
	}

	resp := serveCreate(t, writer, payload, adminPrincipal(), nil)

	require.Equal(t, http.StatusCreated, resp.Code)
	require.Equal(t, 1, writer.calls)
	require.NotNil(t, writer.gotDetails.ImageURL)
	require.Equal(t, imgURL, *writer.gotDetails.ImageURL)

	require.JSONEq(t, `{
		"supplierId": "29e9aa8b-5651-4981-8828-3fbb1b21fcb1",
		"versionId": "173fd7be-4f54-4f15-a8b1-58bd502344a0",
		"name": "The Deck",
		"type": "food",
		"building": "Arts Link",
		"floor": "2",
		"locationDescription": "Beside the central library",
		"latitude": 1.294,
		"longitude": 103.772,
		"openingTime": "08:00",
		"closingTime": "20:00",
		"imageUrl": "https://example.com/supplier.png",
		"available": true,
		"createdAt": "2026-10-01T03:00:00Z",
		"updatedAt": "2026-10-01T03:00:00Z"
	}`, resp.Body.String())
}

func TestCreateSupplierRequiresAuthentication(t *testing.T) {
	writer := &fakeWriter{}

	resp := serveCreate(t, writer, validPayload(), nil, nil)

	require.Equal(t, http.StatusUnauthorized, resp.Code)
	require.Equal(t, 0, writer.calls)
	require.JSONEq(t, `{"code":"UNAUTHENTICATED","message":"authentication is required"}`, resp.Body.String())
}

func TestCreateSupplierRequiresManagePermission(t *testing.T) {
	writer := &fakeWriter{}

	resp := serveCreate(t, writer, validPayload(), normalPrincipal(), nil)

	require.Equal(t, http.StatusForbidden, resp.Code)
	require.Equal(t, 0, writer.calls)
	require.JSONEq(t, `{"code":"FORBIDDEN","message":"insufficient permissions for this operation"}`, resp.Body.String())
}

func TestCreateSupplierRejectsEmptyBody(t *testing.T) {
	writer := &fakeWriter{}

	resp := serveCreate(t, writer, "", adminPrincipal(), nil)

	require.Equal(t, http.StatusBadRequest, resp.Code)
	require.Equal(t, 0, writer.calls)
	var errResp apperror.Error
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&errResp))
	require.Equal(t, apperror.InvalidArgument, errResp.Code)
	require.Equal(t, "request body is required", errResp.Message)
}

func TestCreateSupplierRejectsMalformedJSON(t *testing.T) {
	writer := &fakeWriter{}

	resp := serveCreate(t, writer, `{"name": "broken`, adminPrincipal(), nil)

	require.Equal(t, http.StatusBadRequest, resp.Code)
	require.Equal(t, 0, writer.calls)
	var errResp apperror.Error
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&errResp))
	require.Equal(t, apperror.InvalidArgument, errResp.Code)
	require.Contains(t, errResp.Message, "request body must be valid JSON")
}

func TestCreateSupplierRejectsClientSuppliedSupplierID(t *testing.T) {
	writer := &fakeWriter{}
	payload := validPayload()
	payload["supplierId"] = "29e9aa8b-5651-4981-8828-3fbb1b21fcb1"

	resp := serveCreate(t, writer, payload, adminPrincipal(), nil)

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

func TestCreateSupplierRejectsClientSuppliedVersionID(t *testing.T) {
	writer := &fakeWriter{}
	payload := validPayload()
	payload["versionId"] = "173fd7be-4f54-4f15-a8b1-58bd502344a0"

	resp := serveCreate(t, writer, payload, adminPrincipal(), nil)

	require.Equal(t, http.StatusBadRequest, resp.Code)
	require.Equal(t, 0, writer.calls)

	var errResp apperror.Error
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&errResp))
	require.Equal(t, apperror.InvalidArgument, errResp.Code)
	require.Contains(t, errResp.Fields, apperror.Field{
		Field:   "versionId",
		Message: "must not be provided",
	})
}

func TestCreateSupplierRejectsMissingMandatoryFields(t *testing.T) {
	writer := &fakeWriter{}

	resp := serveCreate(t, writer, map[string]any{}, adminPrincipal(), nil)

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
	require.Contains(t, fieldNames, "type")
	require.Contains(t, fieldNames, "building")
	require.Contains(t, fieldNames, "floor")
	require.Contains(t, fieldNames, "locationDescription")
	require.Contains(t, fieldNames, "latitude")
	require.Contains(t, fieldNames, "longitude")
	require.Contains(t, fieldNames, "openingTime")
	require.Contains(t, fieldNames, "closingTime")
}

func TestCreateSupplierRejectsInvalidCoordinates(t *testing.T) {
	writer := &fakeWriter{}

	t.Run("latitude out of range", func(t *testing.T) {
		payload := validPayload()
		payload["latitude"] = 91.0
		resp := serveCreate(t, writer, payload, adminPrincipal(), nil)
		require.Equal(t, http.StatusBadRequest, resp.Code)

		var errResp apperror.Error
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&errResp))
		require.Contains(t, errResp.Fields, apperror.Field{Field: "latitude", Message: "must be between -90 and 90"})
	})

	t.Run("longitude out of range", func(t *testing.T) {
		payload := validPayload()
		payload["longitude"] = -181.0
		resp := serveCreate(t, writer, payload, adminPrincipal(), nil)
		require.Equal(t, http.StatusBadRequest, resp.Code)

		var errResp apperror.Error
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&errResp))
		require.Contains(t, errResp.Fields, apperror.Field{Field: "longitude", Message: "must be between -180 and 180"})
	})
}

func TestCreateSupplierRejectsInvalidHours(t *testing.T) {
	writer := &fakeWriter{}

	t.Run("malformed clock time", func(t *testing.T) {
		payload := validPayload()
		payload["openingTime"] = "8:00"
		resp := serveCreate(t, writer, payload, adminPrincipal(), nil)
		require.Equal(t, http.StatusBadRequest, resp.Code)

		var errResp apperror.Error
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&errResp))
		require.Contains(t, errResp.Fields, apperror.Field{Field: "openingTime", Message: "must use HH:MM in 24-hour time"})
	})

	t.Run("identical opening and closing times", func(t *testing.T) {
		payload := validPayload()
		payload["openingTime"] = "09:00"
		payload["closingTime"] = "09:00"
		resp := serveCreate(t, writer, payload, adminPrincipal(), nil)
		require.Equal(t, http.StatusBadRequest, resp.Code)

		var errResp apperror.Error
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&errResp))
		require.Contains(t, errResp.Fields, apperror.Field{Field: "closingTime", Message: "must differ from openingTime"})
	})
}

func TestCreateSupplierRejectsInvalidImageURL(t *testing.T) {
	writer := &fakeWriter{}
	payload := validPayload()
	payload["imageUrl"] = "ftp://invalid-scheme.com/logo.png"

	resp := serveCreate(t, writer, payload, adminPrincipal(), nil)

	require.Equal(t, http.StatusBadRequest, resp.Code)
	var errResp apperror.Error
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&errResp))
	require.Contains(t, errResp.Fields, apperror.Field{Field: "imageUrl", Message: "must be an absolute HTTP or HTTPS URL"})
}

func TestCreateSupplierRejectsUnknownFields(t *testing.T) {
	writer := &fakeWriter{}
	payload := validPayload()
	payload["unrecognizedProperty"] = "disallowed"

	resp := serveCreate(t, writer, payload, adminPrincipal(), nil)

	require.Equal(t, http.StatusBadRequest, resp.Code)
	require.Equal(t, 0, writer.calls)
	var errResp apperror.Error
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&errResp))
	require.Equal(t, apperror.InvalidArgument, errResp.Code)
	require.Contains(t, errResp.Message, "unknown field")
}

func TestCreateSupplierMapsNormalizedNameConflict(t *testing.T) {
	writer := &fakeWriter{
		err: apperror.ErrSupplierNameConflict,
	}

	resp := serveCreate(t, writer, validPayload(), adminPrincipal(), nil)

	require.Equal(t, http.StatusConflict, resp.Code)
	require.Equal(t, 1, writer.calls)
	require.JSONEq(t, `{
		"code": "SUPPLIER_NAME_CONFLICT",
		"message": "a supplier with this normalized name already exists"
	}`, resp.Body.String())
}

func TestCreateSupplierMapsDatabaseRaceConflict(t *testing.T) {
	writer := &fakeWriter{
		err: &apperror.Error{
			Code:    apperror.SupplierNameConflict,
			Message: "a supplier with this normalized name already exists",
		},
	}

	resp := serveCreate(t, writer, validPayload(), adminPrincipal(), nil)

	require.Equal(t, http.StatusConflict, resp.Code)
	require.Equal(t, 1, writer.calls)
	require.JSONEq(t, `{
		"code": "SUPPLIER_NAME_CONFLICT",
		"message": "a supplier with this normalized name already exists"
	}`, resp.Body.String())
}

func TestCreateSupplierMasksUnexpectedRepositoryError(t *testing.T) {
	writer := &fakeWriter{
		err: errors.New("database conn string with user=admin password=secret_pw"),
	}

	resp := serveCreate(t, writer, validPayload(), adminPrincipal(), nil)

	require.Equal(t, http.StatusInternalServerError, resp.Code)
	require.Equal(t, 1, writer.calls)
	require.NotContains(t, resp.Body.String(), "secret_pw")
	require.JSONEq(t, `{
		"code": "INTERNAL",
		"message": "an unexpected internal error occurred"
	}`, resp.Body.String())
}
