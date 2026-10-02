package create

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
)

// Handler serves supplier creation requests for administrators.
type Handler struct {
	writer supplier.Writer
	now    func() time.Time
}

// NewHandler constructs a Handler backed by the provided Writer.
func NewHandler(writer supplier.Writer) Handler {
	if writer == nil {
		panic("create: writer is required")
	}
	return Handler{
		writer: writer,
		now:    func() time.Time { return time.Now().UTC() },
	}
}

// NewHandlerWithClock constructs a Handler with a custom clock function for testing.
func NewHandlerWithClock(writer supplier.Writer, now func() time.Time) Handler {
	if writer == nil {
		panic("create: writer is required")
	}
	if now == nil {
		panic("create: clock is required")
	}
	return Handler{
		writer: writer,
		now:    now,
	}
}

// RegisterRoutes registers the creation endpoint on the HTTP mux.
func (h Handler) RegisterRoutes(router *http.ServeMux) {
	router.HandleFunc("POST /suppliers", h.create)
}

type createRequest struct {
	SupplierID          *string  `json:"supplierId"`
	VersionID           *string  `json:"versionId"`
	Name                string   `json:"name"`
	Type                string   `json:"type"`
	Building            string   `json:"building"`
	Floor               string   `json:"floor"`
	LocationDescription string   `json:"locationDescription"`
	Latitude            *float64 `json:"latitude"`
	Longitude           *float64 `json:"longitude"`
	OpeningTime         string   `json:"openingTime"`
	ClosingTime         string   `json:"closingTime"`
	ImageURL            *string  `json:"imageUrl"`
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

func (h Handler) create(response http.ResponseWriter, request *http.Request) {
	principal, ok := auth.PrincipalFromContext(request.Context())
	if !ok {
		writeError(response, http.StatusUnauthorized, apperror.Unauthenticated, "authentication is required")
		return
	}
	if !principal.Has(auth.ManageSuppliers) {
		writeError(response, http.StatusForbidden, apperror.Forbidden, "insufficient permissions for this operation")
		return
	}

	if request.Body == nil {
		writeError(response, http.StatusBadRequest, apperror.InvalidArgument, "request body is required")
		return
	}

	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()

	var req createRequest
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

	details, err := supplier.ValidateCreate(supplier.Create{
		Name:                req.Name,
		Type:                req.Type,
		Building:            req.Building,
		Floor:               req.Floor,
		LocationDescription: req.LocationDescription,
		Latitude:            req.Latitude,
		Longitude:           req.Longitude,
		OpeningTime:         req.OpeningTime,
		ClosingTime:         req.ClosingTime,
		ImageURL:            req.ImageURL,
	})
	if err != nil {
		var valErr *supplier.ValidationError
		if errors.As(err, &valErr) {
			for _, v := range valErr.Violations {
				violations = append(violations, apperror.Field{
					Field:   v.Field,
					Message: v.Message,
				})
			}
		}
	}

	if len(violations) > 0 {
		writeErrorWithFields(response, http.StatusBadRequest, apperror.InvalidArgument, "supplier fields are invalid", violations)
		return
	}

	created, err := h.writer.Create(request.Context(), details, h.now())
	if err != nil {
		var appErr *apperror.Error
		if errors.As(err, &appErr) {
			switch appErr.Code {
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
	response.Header().Set("Location", "/suppliers/"+string(created.SupplierID))
	response.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(response).Encode(supplierResponse{
		SupplierID:          created.SupplierID,
		VersionID:           created.VersionID,
		Name:                created.Details.Name,
		Type:                created.Details.Type,
		Building:            created.Details.Building,
		Floor:               created.Details.Floor,
		LocationDescription: created.Details.LocationDescription,
		Latitude:            created.Details.Latitude,
		Longitude:           created.Details.Longitude,
		OpeningTime:         created.Details.OpeningTime,
		ClosingTime:         created.Details.ClosingTime,
		ImageURL:            created.Details.ImageURL,
		Available:           created.Available,
		CreatedAt:           created.CreatedAt,
		UpdatedAt:           created.UpdatedAt,
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
