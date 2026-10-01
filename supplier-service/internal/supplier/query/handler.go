package query

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
)

type Handler struct {
	service Service
}

func NewHandler(reader supplier.Reader) Handler {
	return Handler{service: NewService(reader)}
}

func (h Handler) RegisterRoutes(router *http.ServeMux) {
	router.HandleFunc("GET /suppliers", h.list)
	router.HandleFunc("GET /suppliers/{supplierId}", h.current)
}

func (h Handler) list(response http.ResponseWriter, request *http.Request) {
	principal, ok := auth.PrincipalFromContext(request.Context())
	if !ok || !principal.Has(auth.ReadSuppliers) {
		writeError(response, authenticationRequired())
		return
	}
	parameters := request.URL.Query()
	filter := supplier.ListFilter{Query: parameters.Get("q"), Type: parameters.Get("type"), Cursor: parameters.Get("cursor")}
	if parameters.Has("limit") {
		var err error
		filter.Limit, err = strconv.Atoi(parameters.Get("limit"))
		if err != nil || filter.Limit < 1 || filter.Limit > supplier.MaximumPageSize {
			writeError(response, &supplier.ValidationError{Violations: []supplier.FieldViolation{{Field: "limit", Message: "must be between 1 and 100"}}})
			return
		}
	}
	page, err := h.service.List(request.Context(), principal, filter)
	if err != nil {
		writeError(response, err)
		return
	}
	result := pageResponse{Items: make([]supplierResponse, 0, len(page.Items)), NextCursor: page.NextCursor}
	for _, item := range page.Items {
		result.Items = append(result.Items, project(item))
	}
	writeJSON(response, http.StatusOK, result)
}

func (h Handler) current(response http.ResponseWriter, request *http.Request) {
	principal, _ := auth.PrincipalFromContext(request.Context())
	item, err := h.service.Current(request.Context(), principal, supplier.SupplierID(request.PathValue("supplierId")))
	if err != nil {
		writeError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, project(item))
}

type pageResponse struct {
	Items      []supplierResponse `json:"items"`
	NextCursor string             `json:"nextCursor,omitempty"`
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

func project(item supplier.Supplier) supplierResponse {
	return supplierResponse{
		SupplierID: item.SupplierID, VersionID: item.VersionID,
		Name: item.Details.Name, Type: item.Details.Type,
		Building: item.Details.Building, Floor: item.Details.Floor,
		LocationDescription: item.Details.LocationDescription,
		Latitude:            item.Details.Latitude, Longitude: item.Details.Longitude,
		OpeningTime: item.Details.OpeningTime, ClosingTime: item.Details.ClosingTime,
		ImageURL: item.Details.ImageURL, Available: item.Available,
		CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func writeError(response http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	result := apperror.Error{Code: apperror.Internal, Message: "an unexpected internal error occurred"}
	var validation *supplier.ValidationError
	var application *apperror.Error
	if errors.As(err, &validation) {
		status = http.StatusBadRequest
		result.Code, result.Message = apperror.InvalidArgument, "supplier fields are invalid"
		for _, violation := range validation.Violations {
			result.Fields = append(result.Fields, apperror.Field{Field: violation.Field, Message: violation.Message})
		}
	} else if errors.As(err, &application) {
		switch application.Code {
		case apperror.InvalidArgument:
			status, result = http.StatusBadRequest, *application
		case apperror.Unauthenticated:
			status, result = http.StatusUnauthorized, *application
		case apperror.SupplierNotFound:
			status, result = http.StatusNotFound, *application
		case apperror.DependencyUnavailable:
			status = http.StatusServiceUnavailable
			result.Code, result.Message = apperror.DependencyUnavailable, "a required dependency is unavailable"
		}
	}
	writeJSON(response, status, result)
}
