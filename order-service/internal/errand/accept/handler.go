// Package accept serves POST /errands/{errandId}/accept (A4): OPEN →
// ASSIGNED for an eligible courier (F3.3). Eligibility comes from the
// courier's withdrawal history and the restriction settings; the transition
// itself, including the pickup and delivery deadlines and the reverting
// credit.adjust when a reward edit is pending (F3.4.2), is errand.Next's.
// Exactly one of competing accepts, cancels and expiries wins through the
// store's version compare-and-swap (F3.3.5).
package accept

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand/errandhttp"
)

// Dependencies are the ports the handler uses; all are required.
type Dependencies struct {
	Store     errand.Store
	Suppliers errand.SupplierGate
	Settings  errand.Settings
	Clock     errand.Clock
	Logger    *slog.Logger
}

type Handler struct {
	store     errand.Store
	suppliers errand.SupplierGate
	settings  errand.Settings
	clock     errand.Clock
	guard     func(http.Handler) http.Handler
	logger    *slog.Logger
}

func NewHandler(deps Dependencies, guard *auth.Middleware) Handler {
	switch {
	case deps.Store == nil:
		panic("accept: store is required")
	case deps.Suppliers == nil:
		panic("accept: supplier gate is required")
	case deps.Settings == nil:
		panic("accept: settings are required")
	case deps.Clock == nil:
		panic("accept: clock is required")
	case deps.Logger == nil:
		panic("accept: logger is required")
	case guard == nil:
		panic("accept: auth middleware is required")
	}
	return Handler{
		store:     deps.Store,
		suppliers: deps.Suppliers,
		settings:  deps.Settings,
		clock:     deps.Clock,
		guard:     guard.RequireUse,
		logger:    deps.Logger,
	}
}

// RegisterRoutes registers the route behind auth.Middleware.RequireUse (a
// user token with errands:use).
func (h Handler) RegisterRoutes(router *http.ServeMux) {
	router.Handle("POST /errands/{errandId}/accept", h.guard(http.HandlerFunc(h.accept)))
}

func (h Handler) accept(response http.ResponseWriter, request *http.Request) {
	ctx := request.Context()
	courier, ok := errandhttp.Caller(response, request)
	if !ok {
		return
	}
	id, err := errandhttp.ErrandID(request)
	if err != nil {
		h.fail(response, request, "", err)
		return
	}
	expectedVersion, err := errandhttp.ReadVersionedAction(response, request)
	if err != nil {
		h.fail(response, request, id, err)
		return
	}

	e, err := h.store.Get(ctx, id)
	if err != nil {
		h.fail(response, request, id, err)
		return
	}
	// A retry of an accept that committed (its response was lost): the
	// errand is ASSIGNED to the caller one version later, which only this
	// accept can have produced. Answer as the first response did.
	acceptedByCaller := func(e errand.Errand) bool {
		return e.Status == errand.StatusAssigned && e.CourierID == courier && e.Version == expectedVersion+1
	}
	if acceptedByCaller(e) {
		h.respond(response, request, e)
		return
	}

	now := h.clock.Now()
	input, err := h.eligibility(ctx, e.ID, courier, now)
	if err != nil {
		h.fail(response, request, id, err)
		return
	}
	actor := errand.Actor{Type: errand.ActorCourier, ID: courier}
	t, err := errand.Next(e, errand.EventAccept, actor, input, now)
	if err != nil {
		h.refuse(response, request, id, courier, expectedVersion, now, err)
		return
	}
	// Checked after Next, so an errand that has moved on is reported as
	// ERRAND_UNAVAILABLE, not as a stale version.
	if e.Version != expectedVersion {
		h.refuse(response, request, id, courier, expectedVersion, now,
			&apperror.Error{Code: apperror.ErrandChanged, Message: "the errand has changed; reload it and try again"})
		return
	}
	stored, err := h.store.Apply(ctx, t)
	if err != nil {
		// The same accept sent twice at once: the copy that lost the
		// compare-and-swap is answered as the one that won.
		if current, lost := errandhttp.LostToDuplicate(ctx, h.store, id, err, acceptedByCaller); lost {
			h.respond(response, request, current)
			return
		}
		h.refuse(response, request, id, courier, expectedVersion, now, err)
		return
	}
	h.respond(response, request, stored)
}

// eligibility builds Next's input for an accept by courier at now: whether
// they ever withdrew from this errand (F3.6.3), whether they are restricted
// (F3.6.4), and the deadline settings (F3.8.4, F3.8.5).
//
// The restriction is "Count withdrawals within Window". CourierHistory gives
// only a count since a time, so Duration is not applied separately: the
// courier is restricted while Count withdrawals fall within the last Window
// (the plan's rule; the defaults have Window = Duration).
func (h Handler) eligibility(ctx context.Context, errandID, courier string, now time.Time) (errand.EventInput, error) {
	settings, err := h.settings.Get(ctx)
	if err != nil {
		return errand.EventInput{}, err
	}
	rule := settings.WithdrawalRestriction
	history, err := h.store.CourierHistory(ctx, errandID, courier, now.Add(-rule.Window))
	if err != nil {
		return errand.EventInput{}, err
	}
	return errand.EventInput{
		PreviouslyWithdrew: history.WithdrewFromErrand,
		Restricted:         history.WithdrawalsSince >= rule.Count,
		Deadlines:          settings.Deadlines,
	}, nil
}

// refuse writes err. A 409 ERRAND_CHANGED or ERRAND_UNAVAILABLE also records
// an errand.accept_rejected event so the courier is notified; 403
// eligibility refusals and other conflicts do not.
func (h Handler) refuse(response http.ResponseWriter, request *http.Request, errandID, courier string, expectedVersion int, now time.Time, err error) {
	var appErr *apperror.Error
	if errors.As(err, &appErr) && (appErr.Code == apperror.ErrandChanged || appErr.Code == apperror.ErrandUnavailable) {
		rejection := errand.Rejection{
			ErrandID:  errandID,
			Action:    errand.RejectedAccept,
			Actor:     errand.Actor{Type: errand.ActorCourier, ID: courier},
			Code:      appErr.Code,
			RequestID: errandhttp.RejectionRequestID(courier, expectedVersion),
			At:        now,
		}
		// The notification is best effort: the courier gets the 409 either
		// way, so a failure is logged rather than turned into a 500.
		if recordErr := h.store.RecordRejection(request.Context(), rejection); recordErr != nil {
			h.logger.ErrorContext(request.Context(), "recording an accept rejection failed",
				slog.String("errand_id", errandID), slog.Any("error", recordErr))
		}
	}
	h.fail(response, request, errandID, err)
}

// respond writes CourierErrandView. If the supplier version cannot be read
// the accept has still committed; a retry gets 200 (see accept).
func (h Handler) respond(response http.ResponseWriter, request *http.Request, e errand.Errand) {
	pickups, err := errandhttp.LoadPickups(request.Context(), h.suppliers, e)
	if err != nil {
		h.fail(response, request, e.ID, err)
		return
	}
	errandhttp.WriteJSON(response, http.StatusOK, errandhttp.CourierView(e, pickups))
}

func (h Handler) fail(response http.ResponseWriter, request *http.Request, errandID string, err error) {
	errandhttp.Fail(response, request, h.logger, "accept", errandID, err)
}
