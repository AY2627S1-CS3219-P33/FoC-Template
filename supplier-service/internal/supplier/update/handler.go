package update

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
)

var clockTimePattern = regexp.MustCompile(`^(?:[01][0-9]|2[0-3]):[0-5][0-9]$`)

// Handler serves supplier update requests for administrators.
type Handler struct {
	writer supplier.Writer
	now    func() time.Time
}

// NewHandler constructs a Handler backed by the provided Writer.
func NewHandler(writer supplier.Writer) Handler {
	if writer == nil {
		panic("update: writer is required")
	}
	return Handler{
		writer: writer,
		now:    func() time.Time { return time.Now().UTC() },
	}
}

// NewHandlerWithClock constructs a Handler with a custom clock function for testing.
func NewHandlerWithClock(writer supplier.Writer, now func() time.Time) Handler {
	if writer == nil {
		panic("update: writer is required")
	}
	if now == nil {
		panic("update: clock is required")
	}
	return Handler{
		writer: writer,
		now:    now,
	}
}

// RegisterRoutes registers the update endpoint on the HTTP mux.
func (h Handler) RegisterRoutes(router *http.ServeMux) {
	router.HandleFunc("PATCH /suppliers/{supplierId}", h.update)
}

type updateRequest struct {
	SupplierID          *string          `json:"supplierId"`
	VersionID           *string          `json:"versionId"`
	Name                *string          `json:"name"`
	Type                *string          `json:"type"`
	Building            *string          `json:"building"`
	Floor               *string          `json:"floor"`
	LocationDescription *string          `json:"locationDescription"`
	Latitude            *float64         `json:"latitude"`
	Longitude           *float64         `json:"longitude"`
	OpeningTime         *string          `json:"openingTime"`
	ClosingTime         *string          `json:"closingTime"`
	ImageURL            *json.RawMessage `json:"imageUrl"`
}

type supplierResponse struct {
	SupplierID          supplier.SupplierID `json:"supplierId"`
	VersionID           supplier.VersionID  `json:"versionId"`
	Name                string              `json:"name"`
	Type                string              `json:"type"`
	Building            string              `json:"building"`
	Floor               string              `json:"floor"`
	LocationDescription string              `json:"locationDescription"`
	Latitude            float64             `json:"latitude"`
	Longitude           float64             `json:"longitude"`
	OpeningTime         string              `json:"openingTime"`
	ClosingTime         string              `json:"closingTime"`
	ImageURL            *string             `json:"imageUrl,omitempty"`
	Available           bool                `json:"available"`
	CreatedAt           time.Time           `json:"createdAt"`
	UpdatedAt           time.Time           `json:"updatedAt"`
}

func (h Handler) update(response http.ResponseWriter, request *http.Request) {
	principal, ok := auth.PrincipalFromContext(request.Context())
	if !ok {
		writeError(response, http.StatusUnauthorized, apperror.Unauthenticated, "authentication is required")
		return
	}
	if !principal.Has(auth.ManageSuppliers) {
		writeError(response, http.StatusForbidden, apperror.Forbidden, "insufficient permissions for this operation")
		return
	}

	supplierID := supplier.SupplierID(request.PathValue("supplierId"))
	if !supplier.ValidSupplierID(supplierID) {
		writeError(response, http.StatusBadRequest, apperror.InvalidArgument, "supplierId must be a UUID")
		return
	}

	if request.Body == nil {
		writeError(response, http.StatusBadRequest, apperror.InvalidArgument, "request body is required")
		return
	}

	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()

	var req updateRequest
	if err := decoder.Decode(&req); err != nil {
		if errors.Is(err, io.EOF) {
			writeError(response, http.StatusBadRequest, apperror.InvalidArgument, "request body is required")
			return
		}
		writeError(response, http.StatusBadRequest, apperror.InvalidArgument, "request body must be valid JSON: "+err.Error())
		return
	}

	violations := make([]apperror.Field, 0)
	if req.SupplierID != nil {
		violations = append(violations, apperror.Field{
			Field:   "supplierId",
			Message: "must not be provided",
		})
	}
	if req.VersionID != nil {
		violations = append(violations, apperror.Field{
			Field:   "versionId",
			Message: "must not be provided",
		})
	}

	patch := supplier.Patch{}

	if req.Name != nil {
		trimmed := strings.TrimSpace(*req.Name)
		if trimmed == "" {
			violations = append(violations, apperror.Field{Field: "name", Message: "is required"})
		} else if utf8.RuneCountInString(*req.Name) > 120 {
			violations = append(violations, apperror.Field{Field: "name", Message: "is too long"})
		} else {
			patch.Name = req.Name
		}
	}

	if req.Type != nil {
		trimmed := strings.TrimSpace(*req.Type)
		if trimmed == "" {
			violations = append(violations, apperror.Field{Field: "type", Message: "is required"})
		} else if utf8.RuneCountInString(*req.Type) > 64 {
			violations = append(violations, apperror.Field{Field: "type", Message: "is too long"})
		} else {
			patch.Type = req.Type
		}
	}

	if req.Building != nil {
		trimmed := strings.TrimSpace(*req.Building)
		if trimmed == "" {
			violations = append(violations, apperror.Field{Field: "building", Message: "is required"})
		} else if utf8.RuneCountInString(*req.Building) > 120 {
			violations = append(violations, apperror.Field{Field: "building", Message: "is too long"})
		} else {
			patch.Building = req.Building
		}
	}

	if req.Floor != nil {
		trimmed := strings.TrimSpace(*req.Floor)
		if trimmed == "" {
			violations = append(violations, apperror.Field{Field: "floor", Message: "is required"})
		} else if utf8.RuneCountInString(*req.Floor) > 32 {
			violations = append(violations, apperror.Field{Field: "floor", Message: "is too long"})
		} else {
			patch.Floor = req.Floor
		}
	}

	if req.LocationDescription != nil {
		trimmed := strings.TrimSpace(*req.LocationDescription)
		if trimmed == "" {
			violations = append(violations, apperror.Field{Field: "locationDescription", Message: "is required"})
		} else if utf8.RuneCountInString(*req.LocationDescription) > 500 {
			violations = append(violations, apperror.Field{Field: "locationDescription", Message: "is too long"})
		} else {
			patch.LocationDescription = req.LocationDescription
		}
	}

	if req.Latitude != nil {
		if math.IsNaN(*req.Latitude) || math.IsInf(*req.Latitude, 0) || *req.Latitude < -90 || *req.Latitude > 90 {
			violations = append(violations, apperror.Field{Field: "latitude", Message: "must be between -90 and 90"})
		} else {
			patch.Latitude = req.Latitude
		}
	}

	if req.Longitude != nil {
		if math.IsNaN(*req.Longitude) || math.IsInf(*req.Longitude, 0) || *req.Longitude < -180 || *req.Longitude > 180 {
			violations = append(violations, apperror.Field{Field: "longitude", Message: "must be between -180 and 180"})
		} else {
			patch.Longitude = req.Longitude
		}
	}

	if req.OpeningTime != nil {
		if *req.OpeningTime == "" {
			violations = append(violations, apperror.Field{Field: "openingTime", Message: "is required"})
		} else if !clockTimePattern.MatchString(*req.OpeningTime) {
			violations = append(violations, apperror.Field{Field: "openingTime", Message: "must use HH:MM in 24-hour time"})
		} else {
			patch.OpeningTime = req.OpeningTime
		}
	}

	if req.ClosingTime != nil {
		if *req.ClosingTime == "" {
			violations = append(violations, apperror.Field{Field: "closingTime", Message: "is required"})
		} else if !clockTimePattern.MatchString(*req.ClosingTime) {
			violations = append(violations, apperror.Field{Field: "closingTime", Message: "must use HH:MM in 24-hour time"})
		} else {
			patch.ClosingTime = req.ClosingTime
		}
	}

	if req.OpeningTime != nil && req.ClosingTime != nil && *req.OpeningTime != "" && *req.OpeningTime == *req.ClosingTime {
		violations = append(violations, apperror.Field{Field: "closingTime", Message: "must differ from openingTime"})
	}

	if req.ImageURL != nil {
		patch.ImageURLSet = true
		rawStr := string(*req.ImageURL)
		if rawStr == "null" {
			patch.ImageURL = nil
		} else {
			var urlStr string
			if err := json.Unmarshal(*req.ImageURL, &urlStr); err != nil {
				violations = append(violations, apperror.Field{Field: "imageUrl", Message: "must be a string or null"})
			} else {
				if utf8.RuneCountInString(urlStr) > 2048 {
					violations = append(violations, apperror.Field{Field: "imageUrl", Message: "is too long"})
				} else {
					parsed, err := url.ParseRequestURI(urlStr)
					if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
						violations = append(violations, apperror.Field{Field: "imageUrl", Message: "must be an absolute HTTP or HTTPS URL"})
					} else {
						patch.ImageURL = &urlStr
					}
				}
			}
		}
	}

	if len(violations) > 0 {
		writeErrorWithFields(response, http.StatusBadRequest, apperror.InvalidArgument, "supplier fields are invalid", violations)
		return
	}

	if patch.Empty() {
		writeError(response, http.StatusBadRequest, apperror.InvalidArgument, "patch must contain at least one field")
		return
	}

	updated, err := h.writer.Update(request.Context(), supplierID, patch, h.now())
	if err != nil {
		var appErr *apperror.Error
		if errors.As(err, &appErr) {
			switch appErr.Code {
			case apperror.SupplierNotFound:
				writeError(response, http.StatusNotFound, appErr.Code, appErr.Message)
				return
			case apperror.SupplierNameConflict:
				writeError(response, http.StatusConflict, appErr.Code, appErr.Message)
				return
			case apperror.InvalidArgument:
				writeErrorWithFields(response, http.StatusBadRequest, appErr.Code, appErr.Message, appErr.Fields)
				return
			}
		}
		var valErr *supplier.ValidationError
		if errors.As(err, &valErr) {
			fields := make([]apperror.Field, len(valErr.Violations))
			for i, v := range valErr.Violations {
				fields[i] = apperror.Field{Field: v.Field, Message: v.Message}
			}
			writeErrorWithFields(response, http.StatusBadRequest, apperror.InvalidArgument, valErr.Error(), fields)
			return
		}
		writeError(response, http.StatusInternalServerError, apperror.Internal, "an unexpected internal error occurred")
		return
	}

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(supplierResponse{
		SupplierID:          updated.SupplierID,
		VersionID:           updated.VersionID,
		Name:                updated.Details.Name,
		Type:                updated.Details.Type,
		Building:            updated.Details.Building,
		Floor:               updated.Details.Floor,
		LocationDescription: updated.Details.LocationDescription,
		Latitude:            updated.Details.Latitude,
		Longitude:           updated.Details.Longitude,
		OpeningTime:         updated.Details.OpeningTime,
		ClosingTime:         updated.Details.ClosingTime,
		ImageURL:            updated.Details.ImageURL,
		Available:           updated.Available,
		CreatedAt:           updated.CreatedAt,
		UpdatedAt:           updated.UpdatedAt,
	})
}

func writeError(response http.ResponseWriter, status int, code apperror.Code, message string) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(apperror.Error{Code: code, Message: message})
}

func writeErrorWithFields(response http.ResponseWriter, status int, code apperror.Code, message string, fields []apperror.Field) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(apperror.Error{
		Code:    code,
		Message: message,
		Fields:  fields,
	})
}
