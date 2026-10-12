package lookup

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

// Handler serves the building and supplier-type lists.
type Handler struct {
	service Service
	now     func() time.Time
}

// NewHandler constructs a Handler backed by the provided LookupStore.
func NewHandler(store supplier.LookupStore) Handler {
	return NewHandlerWithClock(store, func() time.Time { return time.Now().UTC() })
}

// NewHandlerWithClock constructs a Handler with a custom clock function for testing.
func NewHandlerWithClock(store supplier.LookupStore, now func() time.Time) Handler {
	if now == nil {
		panic("lookup: clock is required")
	}
	return Handler{service: NewService(store), now: now}
}

// RegisterRoutes registers list, create and retire for both lookup kinds.
// There is deliberately no rename route: lookup names are immutable.
func (h Handler) RegisterRoutes(router *http.ServeMux) {
	for _, kind := range []Kind{Buildings, SupplierTypes} {
		router.HandleFunc("GET "+kind.path, h.list(kind))
		router.HandleFunc("POST "+kind.path, h.create(kind))
		router.HandleFunc("DELETE "+kind.path+"/{"+kind.idParameter+"}", h.retire(kind))
	}
}

type lookupResponse struct {
	ID      supplier.LookupID `json:"id"`
	Name    string            `json:"name"`
	Retired bool              `json:"retired"`
}

type listResponse struct {
	Items []lookupResponse `json:"items"`
}

type createRequest struct {
	Name string `json:"name"`
}

func (h Handler) list(kind Kind) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		principal, _ := auth.PrincipalFromContext(request.Context())
		if err := requireRead(principal); err != nil {
			writeError(response, kind, err)
			return
		}
		includeRetired := false
		if parameters := request.URL.Query(); parameters.Has("includeRetired") {
			switch parameters.Get("includeRetired") {
			case "true":
				includeRetired = true
			case "false":
			default:
				writeError(response, kind, &supplier.ValidationError{Violations: []supplier.FieldViolation{
					{Field: "includeRetired", Message: "must be true or false"},
				}})
				return
			}
		}
		lookups, err := h.service.List(request.Context(), principal, kind, includeRetired)
		if err != nil {
			writeError(response, kind, err)
			return
		}
		result := listResponse{Items: make([]lookupResponse, 0, len(lookups))}
		for _, item := range lookups {
			result.Items = append(result.Items, project(item))
		}
		writeJSON(response, http.StatusOK, result)
	}
}

func (h Handler) create(kind Kind) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		principal, _ := auth.PrincipalFromContext(request.Context())
		if err := requireManage(principal); err != nil {
			writeError(response, kind, err)
			return
		}
		decoder := json.NewDecoder(request.Body)
		decoder.DisallowUnknownFields()
		var body createRequest
		if err := decoder.Decode(&body); err != nil {
			message := "request body must be valid JSON: " + err.Error()
			if errors.Is(err, io.EOF) {
				message = "request body is required"
			}
			writeError(response, kind, &apperror.Error{Code: apperror.InvalidArgument, Message: message})
			return
		}
		created, err := h.service.Create(request.Context(), principal, kind, body.Name, h.now())
		if err != nil {
			writeError(response, kind, err)
			return
		}
		writeJSON(response, http.StatusCreated, project(created))
	}
}

func (h Handler) retire(kind Kind) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		principal, _ := auth.PrincipalFromContext(request.Context())
		id := supplier.LookupID(request.PathValue(kind.idParameter))
		if err := h.service.Retire(request.Context(), principal, kind, id, h.now()); err != nil {
			writeError(response, kind, err)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	}
}

func project(item supplier.Lookup) lookupResponse {
	return lookupResponse{ID: item.ID, Name: item.Name, Retired: item.Retired}
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func writeError(response http.ResponseWriter, kind Kind, err error) {
	status := http.StatusInternalServerError
	result := apperror.Error{Code: apperror.Internal, Message: "an unexpected internal error occurred"}
	var validation *supplier.ValidationError
	var application *apperror.Error
	if errors.As(err, &validation) {
		status = http.StatusBadRequest
		result.Code, result.Message = apperror.InvalidArgument, string(kind.lookup)+" fields are invalid"
		for _, violation := range validation.Violations {
			result.Fields = append(result.Fields, apperror.Field{Field: violation.Field, Message: violation.Message})
		}
	} else if errors.As(err, &application) {
		switch application.Code {
		case apperror.InvalidArgument:
			status, result = http.StatusBadRequest, *application
		case apperror.Unauthenticated:
			status, result = http.StatusUnauthorized, *application
		case apperror.Forbidden:
			status, result = http.StatusForbidden, *application
		case kind.notFound:
			status, result = http.StatusNotFound, *application
		case kind.conflict, kind.inUse:
			status, result = http.StatusConflict, *application
		case apperror.DependencyUnavailable:
			status = http.StatusServiceUnavailable
			result.Code, result.Message = apperror.DependencyUnavailable, "a required dependency is unavailable"
		}
	}
	writeJSON(response, status, result)
}
