package internalapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"unicode/utf8"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/opsrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/serviceauth"
)

// maxUserIDLength matches the UserID path parameter in openapi/common.yaml.
const maxUserIDLength = 255

// ActiveErrands counts a user's active errands. *opsrepo.Repository
// implements it.
type ActiveErrands interface {
	CountActiveErrandsForUser(ctx context.Context, userID string) (opsrepo.ActiveErrandCounts, error)
}

var _ ActiveErrands = (*opsrepo.Repository)(nil)

// Handler serves the internal read routes to other services.
type Handler struct {
	errands ActiveErrands
	guard   func(http.Handler) http.Handler
	logger  *slog.Logger
}

func NewHandler(errands ActiveErrands, guard *serviceauth.Middleware, logger *slog.Logger) Handler {
	if errands == nil {
		panic("internalapi: active errands is required")
	}
	if guard == nil {
		panic("internalapi: service auth middleware is required")
	}
	if logger == nil {
		panic("internalapi: logger is required")
	}
	return Handler{errands: errands, guard: guard.RequireService(auth.InternalAccess), logger: logger}
}

// RegisterRoutes registers the internal read routes; each passes through
// serviceauth.RequireService(orders:internal) first.
func (h Handler) RegisterRoutes(router *http.ServeMux) {
	router.Handle("GET /internal/users/{userId}/active-errands", h.guard(http.HandlerFunc(h.activeErrands)))
}

type activeErrandsResponse struct {
	UserID      string `json:"userId"`
	AsRequester int    `json:"asRequester"`
	AsCourier   int    `json:"asCourier"`
}

func (h Handler) activeErrands(response http.ResponseWriter, request *http.Request) {
	userID := request.PathValue("userId")
	if userID == "" || len(userID) > maxUserIDLength || !utf8.ValidString(userID) {
		writeError(response, apperror.InvalidArgument, "userId must be between 1 and 255 characters")
		return
	}
	counts, err := h.errands.CountActiveErrandsForUser(request.Context(), userID)
	if err != nil {
		caller := ""
		if principal, ok := auth.PrincipalFromContext(request.Context()); ok {
			caller = principal.Subject
		}
		h.logger.ErrorContext(request.Context(), "active errand count failed",
			slog.String("caller", caller),
			slog.Any("error", err))
		writeError(response, apperror.Internal, "an unexpected internal error occurred")
		return
	}
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(response).Encode(activeErrandsResponse{
		UserID:      userID,
		AsRequester: counts.AsRequester,
		AsCourier:   counts.AsCourier,
	})
}

func writeError(response http.ResponseWriter, code apperror.Code, message string) {
	auth.WriteError(response, code, message)
}
