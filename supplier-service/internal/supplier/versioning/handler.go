package versioning

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
)

type Handler struct {
	reader supplier.Reader
}

func NewHandler(reader supplier.Reader) Handler {
	if reader == nil {
		panic("versioning: reader is required")
	}
	return Handler{reader: reader}
}

func (h Handler) RegisterRoutes(router *http.ServeMux) {
	router.HandleFunc("GET /supplier-versions/{versionId}", h.get)
}

func (h Handler) get(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-Type", "application/json")
	principal, ok := auth.PrincipalFromContext(request.Context())
	if !ok || !principal.Has(auth.ReadSuppliers) {
		writeError(response, http.StatusUnauthorized, apperror.Unauthenticated, "authentication is required")
		return
	}

	versionID := supplier.VersionID(request.PathValue("versionId"))
	if !supplier.ValidVersionID(versionID) {
		writeError(response, http.StatusBadRequest, apperror.InvalidArgument, "versionId must be a UUID")
		return
	}

	version, err := h.reader.GetVersion(request.Context(), versionID)
	if err != nil {
		var applicationError *apperror.Error
		if errors.As(err, &applicationError) && applicationError.Code == apperror.SupplierVersionNotFound {
			writeError(response, http.StatusNotFound, applicationError.Code, applicationError.Message)
			return
		}
		if errors.As(err, &applicationError) && applicationError.Code == apperror.DependencyUnavailable {
			writeError(response, http.StatusServiceUnavailable, apperror.DependencyUnavailable, "a required dependency is unavailable")
			return
		}
		writeError(response, http.StatusInternalServerError, apperror.Internal, "an unexpected internal error occurred")
		return
	}

	_ = json.NewEncoder(response).Encode(versionResponse{
		SupplierID:          version.SupplierID,
		VersionID:           version.VersionID,
		Name:                version.Details.Name,
		Type:                version.Details.Type,
		Building:            version.Details.Building,
		Floor:               version.Details.Floor,
		LocationDescription: version.Details.LocationDescription,
		Latitude:            version.Details.Latitude,
		Longitude:           version.Details.Longitude,
		OpeningTime:         version.Details.OpeningTime,
		ClosingTime:         version.Details.ClosingTime,
		ImageURL:            version.Details.ImageURL,
		Available:           version.Available,
		CreatedAt:           version.CreatedAt,
	})
}

type versionResponse struct {
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
}

func writeError(response http.ResponseWriter, status int, code apperror.Code, message string) {
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(apperror.Error{Code: code, Message: message})
}
