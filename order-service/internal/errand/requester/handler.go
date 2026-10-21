// Package requester serves the requester's errand actions (A5): editing an
// open errand (F3.4, F4.4), cancelling it (F3.5, F4.5.1), and confirming or
// disputing its delivery (F3.10). Each request is decided by errand.Next and
// persisted with Store.Apply's compare-and-swap; credit effects are the
// outbox rows Next returns. A reward change waits for credit-service: the
// edit is stored as pending and the credit-result consumer (M4) applies or
// drops it later.
package requester

import (
	"log/slog"
	"net/http"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand/errandhttp"
)

// Dependencies are the ports the handler uses; all are required.
type Dependencies struct {
	Store     errand.Store
	Suppliers errand.SupplierGate
	Clock     errand.Clock
	Logger    *slog.Logger
}

type Handler struct {
	store     errand.Store
	suppliers errand.SupplierGate
	clock     errand.Clock
	guard     func(http.Handler) http.Handler
	logger    *slog.Logger
}

func NewHandler(deps Dependencies, guard *auth.Middleware) Handler {
	switch {
	case deps.Store == nil:
		panic("requester: store is required")
	case deps.Suppliers == nil:
		panic("requester: supplier gate is required")
	case deps.Clock == nil:
		panic("requester: clock is required")
	case deps.Logger == nil:
		panic("requester: logger is required")
	case guard == nil:
		panic("requester: auth middleware is required")
	}
	return Handler{
		store:     deps.Store,
		suppliers: deps.Suppliers,
		clock:     deps.Clock,
		guard:     guard.RequireUse,
		logger:    deps.Logger,
	}
}

// RegisterRoutes registers the routes behind auth.Middleware.RequireUse (a
// user token with errands:use).
func (h Handler) RegisterRoutes(router *http.ServeMux) {
	router.Handle("PATCH /errands/{errandId}", h.guard(http.HandlerFunc(h.edit)))
	router.Handle("POST /errands/{errandId}/cancel", h.guard(http.HandlerFunc(h.cancel)))
	router.Handle("POST /errands/{errandId}/confirm", h.guard(http.HandlerFunc(h.confirm)))
	router.Handle("POST /errands/{errandId}/dispute", h.guard(http.HandlerFunc(h.dispute)))
}

// ownErrand reads the errand and checks that caller is its requester. Anyone
// else gets FORBIDDEN whatever the errand's state, so the state of someone
// else's errand is never revealed and a stranger can never trigger a
// rejection event. errand.Next checks the relationship again.
func (h Handler) ownErrand(request *http.Request, id, caller string) (errand.Errand, error) {
	e, err := h.store.Get(request.Context(), id)
	if err != nil {
		return errand.Errand{}, err
	}
	if e.RequesterID != caller {
		return errand.Errand{}, &apperror.Error{Code: apperror.Forbidden, Message: "only the errand's requester may do this"}
	}
	return e, nil
}

// staleVersion is the error for an expectedVersion that is not the errand's.
// It is checked after errand.Next, so an errand that has moved on is
// reported as ERRAND_UNAVAILABLE rather than as a stale version.
func staleVersion() error {
	return &apperror.Error{Code: apperror.ErrandChanged, Message: "the errand has changed; reload it and try again"}
}

// respond writes RequesterErrandView. pickups must hold every version
// errandhttp.LoadPickups would load for e.
func (h Handler) respond(response http.ResponseWriter, e errand.Errand, pickups errandhttp.Pickups, status int) {
	errandhttp.WriteJSON(response, status, errandhttp.RequesterView(e, pickups))
}

// respondLoaded writes RequesterErrandView, reading the supplier versions.
func (h Handler) respondLoaded(response http.ResponseWriter, request *http.Request, operation string, e errand.Errand) {
	pickups, err := errandhttp.LoadPickups(request.Context(), h.suppliers, e)
	if err != nil {
		h.fail(response, request, operation, e.ID, err)
		return
	}
	h.respond(response, e, pickups, http.StatusOK)
}

func (h Handler) fail(response http.ResponseWriter, request *http.Request, operation, errandID string, err error) {
	errandhttp.Fail(response, request, h.logger, operation, errandID, err)
}
