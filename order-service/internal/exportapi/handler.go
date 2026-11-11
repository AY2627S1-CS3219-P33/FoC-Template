// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-09
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

package exportapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/opsrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/serviceauth"
)

const (
	// DefaultPageSize and MaxPageSize are the limit parameter's default and
	// maximum in openapi/common.yaml.
	DefaultPageSize = 100
	MaxPageSize     = opsrepo.MaxExportPageSize
	// MaxIDs is the ids parameter's maximum length.
	MaxIDs = 100
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Source reads the export rows; *opsrepo.Repository implements it.
type Source interface {
	ExportTransitions(ctx context.Context, afterID int64, limit int) ([]opsrepo.ExportedTransition, error)
	ExportErrands(ctx context.Context, afterID string, limit int) ([]opsrepo.ExportedErrand, error)
	ExportErrandsByID(ctx context.Context, ids []string) ([]opsrepo.ExportedErrand, error)
}

var _ Source = (*opsrepo.Repository)(nil)

// Suppliers reads immutable supplier versions; errand.SupplierGate
// implements it.
type Suppliers interface {
	GetVersion(ctx context.Context, versionID string) (errand.SupplierVersion, error)
}

// Handler serves the export routes.
type Handler struct {
	source    Source
	suppliers Suppliers
	guard     func(http.Handler) http.Handler
	logger    *slog.Logger
}

func NewHandler(source Source, suppliers Suppliers, guard *serviceauth.Middleware, logger *slog.Logger) Handler {
	switch {
	case source == nil:
		panic("exportapi: source is required")
	case suppliers == nil:
		panic("exportapi: suppliers is required")
	case guard == nil:
		panic("exportapi: service auth middleware is required")
	case logger == nil:
		panic("exportapi: logger is required")
	}
	return Handler{source: source, suppliers: suppliers, guard: guard.RequireService(auth.AdminRead), logger: logger}
}

// RegisterRoutes registers the export routes; each passes through
// serviceauth.RequireService(orders:admin-read) first.
func (h Handler) RegisterRoutes(router *http.ServeMux) {
	router.Handle("GET /internal/export/transitions", h.guard(http.HandlerFunc(h.transitions)))
	router.Handle("GET /internal/export/errands", h.guard(http.HandlerFunc(h.errands)))
}

// ---------------------------------------------------------------------------
// GET /internal/export/transitions
// ---------------------------------------------------------------------------

type transitionResponse struct {
	ID            int64         `json:"id"`
	MessageID     string        `json:"messageId"`
	Type          string        `json:"type"`
	ErrandID      string        `json:"errandId"`
	ErrandVersion int           `json:"errandVersion"`
	Event         errand.Event  `json:"event"`
	From          errand.Status `json:"from,omitempty"`
	To            errand.Status `json:"to"`
	CourierID     string        `json:"courierId,omitempty"`
	Reward        int           `json:"reward"`
	OccurredAt    time.Time     `json:"occurredAt"`
}

type transitionPage struct {
	Items      []transitionResponse `json:"items"`
	NextCursor string               `json:"nextCursor,omitempty"`
}

func (h Handler) transitions(response http.ResponseWriter, request *http.Request) {
	parameters := request.URL.Query()
	limit, ok := parseLimit(response, parameters.Get("limit"), parameters.Has("limit"))
	if !ok {
		return
	}
	var after int64
	if parameters.Has("after") {
		var err error
		after, err = strconv.ParseInt(parameters.Get("after"), 10, 64)
		if err != nil || after < 0 {
			writeError(response, apperror.InvalidArgument, "after must be a non-negative integer")
			return
		}
	}
	// One row more than the page shows whether another page exists.
	rows, err := h.source.ExportTransitions(request.Context(), after, limit+1)
	if err != nil {
		h.fail(response, request, "export transitions", err)
		return
	}
	body := transitionPage{Items: make([]transitionResponse, 0, min(len(rows), limit))}
	for i, t := range rows {
		if i == limit {
			body.NextCursor = strconv.FormatInt(rows[limit-1].ID, 10)
			break
		}
		body.Items = append(body.Items, transitionResponse{
			ID: t.ID, MessageID: t.MessageID, Type: t.Type, ErrandID: t.ErrandID,
			ErrandVersion: t.ErrandVersion, Event: t.Event, From: t.From, To: t.To,
			CourierID: t.CourierID, Reward: t.Reward, OccurredAt: t.OccurredAt,
		})
	}
	writeJSON(response, http.StatusOK, body)
}

// ---------------------------------------------------------------------------
// GET /internal/export/errands
// ---------------------------------------------------------------------------

// errandResponse has null courierId, supplierName and deliveryBuildingName
// when the value is unknown.
type errandResponse struct {
	ErrandID             string        `json:"errandId"`
	Status               errand.Status `json:"status"`
	Version              int           `json:"version"`
	CourierID            *string       `json:"courierId"`
	Reward               int           `json:"reward"`
	SupplierID           string        `json:"supplierId"`
	SupplierName         *string       `json:"supplierName"`
	DeliveryBuildingID   string        `json:"deliveryBuildingId"`
	DeliveryBuildingName *string       `json:"deliveryBuildingName"`
	CreatedAt            time.Time     `json:"createdAt"`
	UpdatedAt            time.Time     `json:"updatedAt"`
}

type errandPage struct {
	Items      []errandResponse `json:"items"`
	NextCursor string           `json:"nextCursor,omitempty"`
}

func (h Handler) errands(response http.ResponseWriter, request *http.Request) {
	parameters := request.URL.Query()
	var (
		rows []opsrepo.ExportedErrand
		next string
		err  error
	)
	if parameters.Has("ids") {
		if parameters.Has("after") || parameters.Has("limit") {
			writeError(response, apperror.InvalidArgument, "ids cannot be combined with after or limit")
			return
		}
		ids, ok := parseIDs(response, parameters.Get("ids"))
		if !ok {
			return
		}
		rows, err = h.source.ExportErrandsByID(request.Context(), ids)
	} else {
		limit, ok := parseLimit(response, parameters.Get("limit"), parameters.Has("limit"))
		if !ok {
			return
		}
		after := ""
		if parameters.Has("after") {
			after = parameters.Get("after")
			if !uuidPattern.MatchString(after) {
				writeError(response, apperror.InvalidArgument, "after must be an errand ID")
				return
			}
			after = strings.ToLower(after)
		}
		rows, err = h.source.ExportErrands(request.Context(), after, limit+1)
		if err == nil && len(rows) > limit {
			rows = rows[:limit]
			next = rows[limit-1].ID
		}
	}
	if err != nil {
		h.fail(response, request, "export errands", err)
		return
	}
	names := h.supplierNames(request.Context(), rows)
	body := errandPage{Items: make([]errandResponse, 0, len(rows)), NextCursor: next}
	for _, e := range rows {
		body.Items = append(body.Items, errandResponse{
			ErrandID: e.ID, Status: e.Status, Version: e.Version, CourierID: nonEmpty(e.CourierID),
			Reward: e.Reward, SupplierID: e.SupplierID, SupplierName: names[e.SupplierVersionID],
			DeliveryBuildingID: e.DeliveryBuildingID, DeliveryBuildingName: nonEmpty(e.DeliveryBuildingName),
			CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt,
		})
	}
	writeJSON(response, http.StatusOK, body)
}

// supplierNames resolves each distinct supplier version once, by version ID,
// so the name is the supplier's as it was when the errand was created, also
// for a supplier since changed or deleted. A version supplier-service does
// not know has no name. If supplier-service itself fails, the remaining
// versions are left unresolved without more calls: the rows are still
// returned, and admin-service fills the names on a later refresh.
func (h Handler) supplierNames(ctx context.Context, rows []opsrepo.ExportedErrand) map[string]*string {
	names := map[string]*string{}
	for _, e := range rows {
		id := e.SupplierVersionID
		if _, done := names[id]; done {
			continue
		}
		version, err := h.suppliers.GetVersion(ctx, id)
		if err != nil {
			var appErr *apperror.Error
			if errors.As(err, &appErr) && appErr.Code == apperror.SupplierUnavailable {
				names[id] = nil
				continue
			}
			h.logger.WarnContext(ctx, "export: supplier names unavailable", slog.Any("error", err))
			return names
		}
		names[id] = nonEmpty(version.Name)
	}
	return names
}

// ---------------------------------------------------------------------------
// Shared
// ---------------------------------------------------------------------------

func nonEmpty(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func parseLimit(response http.ResponseWriter, value string, present bool) (int, bool) {
	if !present {
		return DefaultPageSize, true
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit < 1 || limit > MaxPageSize {
		writeError(response, apperror.InvalidArgument, "limit must be between 1 and "+strconv.Itoa(MaxPageSize))
		return 0, false
	}
	return limit, true
}

func parseIDs(response http.ResponseWriter, value string) ([]string, bool) {
	parts := strings.Split(value, ",")
	if value == "" || len(parts) > MaxIDs {
		writeError(response, apperror.InvalidArgument, "ids must list between 1 and "+strconv.Itoa(MaxIDs)+" errand IDs")
		return nil, false
	}
	ids := make([]string, 0, len(parts))
	for _, part := range parts {
		if !uuidPattern.MatchString(part) {
			writeError(response, apperror.InvalidArgument, "ids must be comma-separated errand IDs")
			return nil, false
		}
		ids = append(ids, strings.ToLower(part))
	}
	return ids, true
}

func (h Handler) fail(response http.ResponseWriter, request *http.Request, operation string, err error) {
	if errors.Is(err, opsrepo.ErrInvalidArgument) {
		writeError(response, apperror.InvalidArgument, "the request is invalid")
		return
	}
	caller := ""
	if principal, ok := auth.PrincipalFromContext(request.Context()); ok {
		caller = principal.Subject
	}
	h.logger.ErrorContext(request.Context(), "export request failed",
		slog.String("operation", operation), slog.String("caller", caller), slog.Any("error", err))
	writeError(response, apperror.Internal, "an unexpected internal error occurred")
}

func writeJSON(response http.ResponseWriter, status int, body any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(body)
}

func writeError(response http.ResponseWriter, code apperror.Code, message string) {
	auth.WriteError(response, code, message)
}
