package fence

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/serviceauth"
)

// maxBodyBytes bounds the Acquire body, which holds only a supplier ID.
const maxBodyBytes = 4 << 10

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Handler serves the fence routes to other services.
type Handler struct {
	fences Fences
	guard  func(http.Handler) http.Handler
	logger *slog.Logger
}

func NewHandler(fences Fences, guard *serviceauth.Middleware, logger *slog.Logger) Handler {
	if fences == nil {
		panic("fence: fences is required")
	}
	if guard == nil {
		panic("fence: service auth middleware is required")
	}
	if logger == nil {
		panic("fence: logger is required")
	}
	return Handler{fences: fences, guard: guard.RequireService(auth.InternalAccess), logger: logger}
}

// RegisterRoutes registers the four fence routes; each passes through
// serviceauth.RequireService(orders:internal) first.
func (h Handler) RegisterRoutes(router *http.ServeMux) {
	router.Handle("POST /internal/supplier-fences/{operationId}/acquire", h.guard(http.HandlerFunc(h.acquire)))
	router.Handle("GET /internal/supplier-fences/{operationId}", h.guard(http.HandlerFunc(h.inspect)))
	router.Handle("POST /internal/supplier-fences/{operationId}/commit", h.guard(http.HandlerFunc(h.commit)))
	router.Handle("POST /internal/supplier-fences/{operationId}/release", h.guard(http.HandlerFunc(h.release)))
}

type acquireRequest struct {
	SupplierID *string `json:"supplierId"`
}

type fenceResponse struct {
	OperationID      string `json:"operationId"`
	SupplierID       string `json:"supplierId"`
	State            State  `json:"state"`
	HasActiveErrands bool   `json:"hasActiveErrands"`
}

func (h Handler) acquire(response http.ResponseWriter, request *http.Request) {
	actor, operationID, ok := h.target(response, request)
	if !ok {
		return
	}
	supplierID, ok := decodeAcquire(response, request)
	if !ok {
		return
	}
	fence, err := h.fences.Acquire(request.Context(), actor, operationID, supplierID)
	h.respond(response, request, "acquire", operationID, fence, err)
}

func (h Handler) inspect(response http.ResponseWriter, request *http.Request) {
	_, operationID, ok := h.target(response, request)
	if !ok {
		return
	}
	fence, err := h.fences.Inspect(request.Context(), operationID)
	h.respond(response, request, "inspect", operationID, fence, err)
}

func (h Handler) commit(response http.ResponseWriter, request *http.Request) {
	actor, operationID, ok := h.target(response, request)
	if !ok {
		return
	}
	fence, err := h.fences.Commit(request.Context(), actor, operationID)
	h.respond(response, request, "commit", operationID, fence, err)
}

func (h Handler) release(response http.ResponseWriter, request *http.Request) {
	actor, operationID, ok := h.target(response, request)
	if !ok {
		return
	}
	fence, err := h.fences.Release(request.Context(), actor, operationID)
	h.respond(response, request, "release", operationID, fence, err)
}

// target returns the calling service's sub and the validated operation ID.
func (h Handler) target(response http.ResponseWriter, request *http.Request) (actor, operationID string, ok bool) {
	principal, found := auth.PrincipalFromContext(request.Context())
	if !found || principal.Subject == "" {
		writeError(response, apperror.Unauthenticated, "authentication is required")
		return "", "", false
	}
	operationID = request.PathValue("operationId")
	if !uuidPattern.MatchString(operationID) {
		writeError(response, apperror.InvalidArgument, "operationId must be a UUID")
		return "", "", false
	}
	return principal.Subject, strings.ToLower(operationID), true
}

func decodeAcquire(response http.ResponseWriter, request *http.Request) (string, bool) {
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()

	var body acquireRequest
	if err := decoder.Decode(&body); err != nil {
		var typeErr *json.UnmarshalTypeError
		switch {
		case errors.Is(err, io.EOF):
			writeError(response, apperror.InvalidArgument, "request body is required")
		case errors.As(err, &typeErr) && typeErr.Field == "supplierId":
			writeValidation(response, apperror.Field{Field: "supplierId", Message: "must be a UUID string"})
		case strings.HasPrefix(err.Error(), "json: unknown field "):
			field := strings.Trim(strings.TrimPrefix(err.Error(), "json: unknown field "), `"`)
			writeValidation(response, apperror.Field{Field: field, Message: "is not allowed"})
		default:
			writeError(response, apperror.InvalidArgument, "request body must be a JSON object")
		}
		return "", false
	}
	if decoder.More() {
		writeError(response, apperror.InvalidArgument, "request body must be a single JSON object")
		return "", false
	}
	switch {
	case body.SupplierID == nil:
		writeValidation(response, apperror.Field{Field: "supplierId", Message: "is required"})
		return "", false
	case !uuidPattern.MatchString(*body.SupplierID):
		writeValidation(response, apperror.Field{Field: "supplierId", Message: "must be a UUID"})
		return "", false
	}
	return strings.ToLower(*body.SupplierID), true
}

func (h Handler) respond(response http.ResponseWriter, request *http.Request, operation, operationID string, fence Fence, err error) {
	if err != nil {
		var appErr *apperror.Error
		if errors.As(err, &appErr) {
			writeError(response, appErr.Code, appErr.Message)
			return
		}
		h.logger.ErrorContext(request.Context(), "fence request failed",
			slog.String("operation", operation),
			slog.String("operation_id", operationID),
			slog.Any("error", err))
		writeError(response, apperror.Internal, "an unexpected internal error occurred")
		return
	}
	if operation != "inspect" {
		h.logger.InfoContext(request.Context(), "fence "+operation,
			slog.String("operation_id", fence.OperationID),
			slog.String("supplier_id", fence.SupplierID),
			slog.String("state", string(fence.State)),
			slog.Bool("has_active_errands", fence.HasActiveErrands))
	}
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(fenceResponse{
		OperationID:      fence.OperationID,
		SupplierID:       fence.SupplierID,
		State:            fence.State,
		HasActiveErrands: fence.HasActiveErrands,
	})
}

func writeError(response http.ResponseWriter, code apperror.Code, message string) {
	auth.WriteError(response, code, message)
}

func writeValidation(response http.ResponseWriter, fields ...apperror.Field) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(apperror.HTTPStatus(apperror.ValidationFailed))
	_ = json.NewEncoder(response).Encode(apperror.Error{
		Code:    apperror.ValidationFailed,
		Message: "request body is invalid",
		Fields:  fields,
	})
}
