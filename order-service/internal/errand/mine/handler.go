// Package mine serves the caller's own errand lists (A7):
//
//   - GET /me/errands/requested: the caller's active requested errands,
//     newest first (F3.11, F3.11.1), in the requester view.
//   - GET /me/errands/delivering: the caller's active deliveries, most
//     recently updated first (F3.12), in the courier view.
//   - GET /me/errands/history: the caller's terminal errands as requester or
//     courier, most recently updated first (F3.13), each in the view of the
//     caller's role in it.
//
// The caller is always the token's subject; no list takes a user ID
// (NFR3.3.1). Every list reads the store, never the cache, and pickup
// details come from the errand's stored supplier version, so they stay
// readable after the supplier is deleted (F2.4.3).
package mine

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand/errandhttp"
)

// Dependencies are the ports the handler uses; all are required.
type Dependencies struct {
	Store     errand.Store
	Suppliers errand.SupplierGate
	Logger    *slog.Logger
}

type Handler struct {
	store     errand.Store
	suppliers errand.SupplierGate
	guard     func(http.Handler) http.Handler
	logger    *slog.Logger
}

func NewHandler(deps Dependencies, guard *auth.Middleware) Handler {
	switch {
	case deps.Store == nil:
		panic("mine: store is required")
	case deps.Suppliers == nil:
		panic("mine: supplier gate is required")
	case deps.Logger == nil:
		panic("mine: logger is required")
	case guard == nil:
		panic("mine: auth middleware is required")
	}
	return Handler{
		store:     deps.Store,
		suppliers: deps.Suppliers,
		guard:     guard.RequireUse,
		logger:    deps.Logger,
	}
}

// RegisterRoutes registers the three lists behind auth.Middleware.RequireUse
// (a user token with errands:use).
func (h Handler) RegisterRoutes(router *http.ServeMux) {
	router.Handle("GET /me/errands/requested", h.guard(http.HandlerFunc(h.requested)))
	router.Handle("GET /me/errands/delivering", h.guard(http.HandlerFunc(h.delivering)))
	router.Handle("GET /me/errands/history", h.guard(http.HandlerFunc(h.history)))
}

// list describes one of the caller's lists.
type list struct {
	// name binds the list's cursors, whose At means different things per
	// list, and names the list in logs.
	name string
	load func(errand.Store, context.Context, errand.UserListQuery) (errand.Page[errand.UserListCursor], error)
	// view is the response item for e listed for caller.
	view func(e errand.Errand, caller string, pickups errandhttp.Pickups) any
}

// requestedList holds only errands the caller requested, so every item is
// the requester view: their own contact person, nothing about the courier
// (NFR3.6.4).
var requestedList = list{
	name: "requested",
	load: errand.Store.ListRequested,
	view: func(e errand.Errand, _ string, pickups errandhttp.Pickups) any {
		return errandhttp.RequesterView(e, pickups)
	},
}

// deliveringList holds only active errands assigned to the caller, so every
// item is the courier view, with the contact person while the courier needs
// it (NFR3.6.4).
var deliveringList = list{
	name: "delivering",
	load: errand.Store.ListDelivering,
	view: func(e errand.Errand, _ string, pickups errandhttp.Pickups) any {
		return errandhttp.CourierView(e, pickups)
	},
}

// historyList mixes both roles. The courier view of a terminal errand never
// has the contact person. A requester cannot accept their own errand, so the
// caller is never both.
var historyList = list{
	name: "history",
	load: errand.Store.ListHistory,
	view: func(e errand.Errand, caller string, pickups errandhttp.Pickups) any {
		if e.RequesterID == caller {
			return errandhttp.RequesterView(e, pickups)
		}
		return errandhttp.CourierView(e, pickups)
	},
}

func (h Handler) requested(response http.ResponseWriter, request *http.Request) {
	h.list(response, request, requestedList)
}

func (h Handler) delivering(response http.ResponseWriter, request *http.Request) {
	h.list(response, request, deliveringList)
}

func (h Handler) history(response http.ResponseWriter, request *http.Request) {
	h.list(response, request, historyList)
}

// page is RequesterErrandPage, CourierErrandPage and HistoryPage.
type page struct {
	Items      []any  `json:"items"`
	NextCursor string `json:"nextCursor,omitempty"`
}

// list reads one page of l for the caller and the supplier versions its
// items need. A supplier-service failure fails the whole page (503), rather
// than returning items without their pickup location.
func (h Handler) list(response http.ResponseWriter, request *http.Request, l list) {
	ctx := request.Context()
	caller, ok := errandhttp.Caller(response, request)
	if !ok {
		return
	}
	operation := "list " + l.name
	q, err := parseQuery(request.URL.Query(), l.name)
	if err != nil {
		h.fail(response, request, operation, err)
		return
	}
	q.UserID = caller

	p, err := l.load(h.store, ctx, q)
	if err != nil {
		h.fail(response, request, operation, err)
		return
	}
	pickups, err := errandhttp.LoadPickups(ctx, h.suppliers, p.Items...)
	if err != nil {
		h.fail(response, request, operation, err)
		return
	}
	out := page{Items: make([]any, 0, len(p.Items))}
	for _, e := range p.Items {
		out.Items = append(out.Items, l.view(e, caller, pickups))
	}
	if p.Next != nil {
		out.NextCursor = encodeCursor(l.name, *p.Next)
	}
	errandhttp.WriteJSON(response, http.StatusOK, out)
}

func (h Handler) fail(response http.ResponseWriter, request *http.Request, operation string, err error) {
	errandhttp.Fail(response, request, h.logger, operation, "", err)
}
