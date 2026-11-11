// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-09
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

package places

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand/errandhttp"
)

// Dependencies are the ports the handler uses; all are required.
type Dependencies struct {
	Store     Store
	Suppliers errand.SupplierGate
	Clock     errand.Clock
	Logger    *slog.Logger
}

// Handler serves the caller's saved places:
//
//   - GET /me/places: the caller's places, newest first, each marked
//     available or not against supplier-service's current data.
//   - POST /me/places: save a place whose building is live or whose
//     supplier exists.
//   - PATCH /me/places/{placeId}: rename a place or change its delivery
//     detail.
//   - DELETE /me/places/{placeId}: remove a place.
//
// The caller is always the token's subject; no route takes a user ID
// (NFR3.3.1), and another user's place is PLACE_NOT_FOUND.
type Handler struct {
	store     Store
	suppliers errand.SupplierGate
	clock     errand.Clock
	guard     func(http.Handler) http.Handler
	logger    *slog.Logger
}

func NewHandler(deps Dependencies, guard *auth.Middleware) Handler {
	switch {
	case deps.Store == nil:
		panic("places: store is required")
	case deps.Suppliers == nil:
		panic("places: supplier gate is required")
	case deps.Clock == nil:
		panic("places: clock is required")
	case deps.Logger == nil:
		panic("places: logger is required")
	case guard == nil:
		panic("places: auth middleware is required")
	}
	return Handler{
		store:     deps.Store,
		suppliers: deps.Suppliers,
		clock:     deps.Clock,
		guard:     guard.RequireUse,
		logger:    deps.Logger,
	}
}

// RegisterRoutes registers the four routes behind auth.Middleware.RequireUse
// (a user token with errands:use).
func (h Handler) RegisterRoutes(router *http.ServeMux) {
	router.Handle("GET /me/places", h.guard(http.HandlerFunc(h.list)))
	router.Handle("POST /me/places", h.guard(http.HandlerFunc(h.create)))
	router.Handle("PATCH /me/places/{placeId}", h.guard(http.HandlerFunc(h.update)))
	router.Handle("DELETE /me/places/{placeId}", h.guard(http.HandlerFunc(h.delete)))
}

// page is PlaceList. Places are capped per kind, so it is never paged.
type page struct {
	Items []item `json:"items"`
}

// list answers every place of the caller, or of one kind. A supplier-service
// failure fails the whole list (503), rather than guessing which places are
// still available.
func (h Handler) list(response http.ResponseWriter, request *http.Request) {
	ctx := request.Context()
	caller, ok := errandhttp.Caller(response, request)
	if !ok {
		return
	}
	kind, err := parseKindQuery(request.URL.Query())
	if err != nil {
		h.fail(response, request, "list places", err)
		return
	}
	stored, err := h.store.List(ctx, caller, kind)
	if err != nil {
		h.fail(response, request, "list places", err)
		return
	}
	found, err := h.resolve(ctx, stored)
	if err != nil {
		h.fail(response, request, "list places", err)
		return
	}
	out := page{Items: make([]item, 0, len(stored))}
	for _, p := range stored {
		out.Items = append(out.Items, found.item(p))
	}
	errandhttp.WriteJSON(response, http.StatusOK, out)
}

// create answers 201 with the new place. The building or supplier is checked
// against supplier-service first; a closed supplier can be saved.
func (h Handler) create(response http.ResponseWriter, request *http.Request) {
	ctx := request.Context()
	caller, ok := errandhttp.Caller(response, request)
	if !ok {
		return
	}
	raw, err := errandhttp.ReadObject(response, request, maxPlaceBytes)
	if err != nil {
		h.fail(response, request, "create place", err)
		return
	}
	p, err := parseNewPlace(raw)
	if err != nil {
		h.fail(response, request, "create place", err)
		return
	}
	p.UserID = caller

	found := lookups{}
	switch p.Kind {
	case KindDelivery:
		building, err := errandhttp.DeliveryBuilding(ctx, h.suppliers, p.DeliveryBuildingID)
		if err != nil {
			h.fail(response, request, "create place", err)
			return
		}
		found.buildings = map[string]errand.Building{strings.ToLower(building.ID): building}
	case KindPickup:
		version, err := h.suppliers.GetCurrent(ctx, p.SupplierID)
		if hasCode(err, apperror.SupplierUnavailable) {
			err = invalid("supplierId", "must be an existing supplier")
		}
		if err != nil {
			h.fail(response, request, "create place", err)
			return
		}
		found.suppliers = map[string]*errand.SupplierVersion{p.SupplierID: &version}
	}

	stored, err := h.store.Create(ctx, p)
	if err != nil {
		h.fail(response, request, "create place", err)
		return
	}
	errandhttp.WriteJSON(response, http.StatusCreated, found.item(stored))
}

// update answers 200 with the changed place, resolved like a list item.
func (h Handler) update(response http.ResponseWriter, request *http.Request) {
	ctx := request.Context()
	caller, ok := errandhttp.Caller(response, request)
	if !ok {
		return
	}
	id, err := placeID(request)
	if err != nil {
		h.fail(response, request, "update place", err)
		return
	}
	raw, err := errandhttp.ReadObject(response, request, maxPlaceBytes)
	if err != nil {
		h.fail(response, request, "update place", err)
		return
	}
	patch, err := parsePatch(raw)
	if err != nil {
		h.fail(response, request, "update place", err)
		return
	}
	stored, err := h.store.Update(ctx, caller, id, patch)
	if err != nil {
		h.fail(response, request, "update place", err)
		return
	}
	found, err := h.resolve(ctx, []Place{stored})
	if err != nil {
		h.fail(response, request, "update place", err)
		return
	}
	errandhttp.WriteJSON(response, http.StatusOK, found.item(stored))
}

// delete answers 204.
func (h Handler) delete(response http.ResponseWriter, request *http.Request) {
	caller, ok := errandhttp.Caller(response, request)
	if !ok {
		return
	}
	id, err := placeID(request)
	if err != nil {
		h.fail(response, request, "delete place", err)
		return
	}
	if err := h.store.Delete(request.Context(), caller, id); err != nil {
		h.fail(response, request, "delete place", err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

// placeID reads the placeId path value in lower case. A malformed one is
// PLACE_NOT_FOUND, like a place that does not exist.
func placeID(request *http.Request) (string, error) {
	id := request.PathValue("placeId")
	if !errandhttp.UUIDPattern.MatchString(id) {
		return "", NotFound()
	}
	return strings.ToLower(id), nil
}

// fail writes err. Only the operation and error are logged, never the
// caller, labels or delivery details.
func (h Handler) fail(response http.ResponseWriter, request *http.Request, operation string, err error) {
	errandhttp.Fail(response, request, h.logger, operation, "", err)
}

func hasCode(err error, code apperror.Code) bool {
	var appErr *apperror.Error
	return errors.As(err, &appErr) && appErr.Code == code
}
