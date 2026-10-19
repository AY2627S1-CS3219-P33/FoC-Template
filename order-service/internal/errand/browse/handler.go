// Package browse serves the read-only errand routes of A3:
//
//   - GET /errands/open lists open errands for couriers. It reads the whole
//     open set through errand.OpenErrandCache (a few seconds stale at most)
//     and filters, orders and pages it per request, so courier coordinates
//     never reach the cache (F3.2, NFR3.6.4).
//   - GET /errands/{errandId} returns one errand from the store, never the
//     cache, in the view the caller's relationship to it allows.
package browse

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"golang.org/x/sync/singleflight"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand/errandhttp"
)

// Dependencies are the ports the handler uses; all are required. Cache may be
// a no-op cache that always misses.
type Dependencies struct {
	Store     errand.Store
	Suppliers errand.SupplierGate
	Cache     errand.OpenErrandCache
	Clock     errand.Clock
	Logger    *slog.Logger
}

type Handler struct {
	store     errand.Store
	suppliers errand.SupplierGate
	clock     errand.Clock
	open      openSet
	guard     func(http.Handler) http.Handler
	logger    *slog.Logger
}

func NewHandler(deps Dependencies, guard *auth.Middleware) Handler {
	switch {
	case deps.Store == nil:
		panic("browse: store is required")
	case deps.Suppliers == nil:
		panic("browse: supplier gate is required")
	case deps.Cache == nil:
		panic("browse: open-errand cache is required")
	case deps.Clock == nil:
		panic("browse: clock is required")
	case deps.Logger == nil:
		panic("browse: logger is required")
	case guard == nil:
		panic("browse: auth middleware is required")
	}
	return Handler{
		store:     deps.Store,
		suppliers: deps.Suppliers,
		clock:     deps.Clock,
		open: openSet{
			store:     deps.Store,
			suppliers: deps.Suppliers,
			cache:     deps.Cache,
			clock:     deps.Clock,
			logger:    deps.Logger,
			loads:     &singleflight.Group{},
		},
		guard:  guard.RequireUse,
		logger: deps.Logger,
	}
}

// RegisterRoutes registers both routes behind auth.Middleware.RequireUse (a
// user token with errands:use). The literal /errands/open takes precedence
// over /errands/{errandId}.
func (h Handler) RegisterRoutes(router *http.ServeMux) {
	router.Handle("GET /errands/open", h.guard(http.HandlerFunc(h.list)))
	router.Handle("GET /errands/{errandId}", h.guard(http.HandlerFunc(h.get)))
}

// list never logs the query: it may hold the courier's coordinates.
func (h Handler) list(response http.ResponseWriter, request *http.Request) {
	if _, ok := errandhttp.Caller(response, request); !ok {
		return
	}
	q, err := parseListQuery(request.URL.Query())
	if err != nil {
		h.fail(response, request, "list open", "", err)
		return
	}
	views, err := h.open.get(request.Context())
	if err != nil {
		h.fail(response, request, "list open", "", err)
		return
	}
	errandhttp.WriteJSON(response, http.StatusOK, page(views, q, h.clock.Now()))
}

// get answers with RequesterErrandView for the requester and
// CourierErrandView for the assigned courier, in any state; anyone else gets
// PublicErrandView while the errand is OPEN and ERRAND_NOT_FOUND otherwise,
// so the existence of an assigned errand is not disclosed.
func (h Handler) get(response http.ResponseWriter, request *http.Request) {
	caller, ok := errandhttp.Caller(response, request)
	if !ok {
		return
	}
	id := request.PathValue("errandId")
	if !uuidPattern.MatchString(id) {
		errandhttp.WriteError(response, apperror.InvalidArgument, "errandId must be a UUID")
		return
	}
	id = strings.ToLower(id)

	e, err := h.store.Get(request.Context(), id)
	if err != nil {
		h.fail(response, request, "get", id, err)
		return
	}
	isRequester := caller == e.RequesterID
	isCourier := e.CourierID != "" && caller == e.CourierID
	if !isRequester && !isCourier && e.Status != errand.StatusOpen {
		errandhttp.WriteError(response, apperror.ErrandNotFound, fmt.Sprintf("errand %q not found", id))
		return
	}
	pickups, err := errandhttp.LoadPickups(request.Context(), h.suppliers, e)
	if err != nil {
		h.fail(response, request, "get", id, err)
		return
	}
	switch {
	case isRequester:
		errandhttp.WriteJSON(response, http.StatusOK, errandhttp.RequesterView(e, pickups))
	case isCourier:
		errandhttp.WriteJSON(response, http.StatusOK, errandhttp.CourierView(e, pickups))
	default:
		errandhttp.WriteJSON(response, http.StatusOK, errandhttp.PublicView(e, pickups))
	}
}

func (h Handler) fail(response http.ResponseWriter, request *http.Request, operation, errandID string, err error) {
	errandhttp.Fail(response, request, h.logger, operation, errandID, err)
}
