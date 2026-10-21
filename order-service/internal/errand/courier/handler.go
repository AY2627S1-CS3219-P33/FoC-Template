// Package courier serves the assigned courier's errand actions (A6):
// withdrawing (F3.6, F3.6.1, F4.3.3), picking up (F3.8.1), delivering
// (F3.8.2) and reporting an issue after pickup (F3.6.2). Each request is
// decided by errand.Next and persisted with Store.Apply's compare-and-swap;
// credit effects (the release when a withdrawal expires the errand) are the
// outbox rows Next returns.
package courier

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
		panic("courier: store is required")
	case deps.Suppliers == nil:
		panic("courier: supplier gate is required")
	case deps.Settings == nil:
		panic("courier: settings are required")
	case deps.Clock == nil:
		panic("courier: clock is required")
	case deps.Logger == nil:
		panic("courier: logger is required")
	case guard == nil:
		panic("courier: auth middleware is required")
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

// RegisterRoutes registers the routes behind auth.Middleware.RequireUse (a
// user token with errands:use).
func (h Handler) RegisterRoutes(router *http.ServeMux) {
	router.Handle("POST /errands/{errandId}/withdraw", h.guard(http.HandlerFunc(h.withdraw)))
	router.Handle("POST /errands/{errandId}/pickup", h.guard(http.HandlerFunc(h.pickUp)))
	router.Handle("POST /errands/{errandId}/deliver", h.guard(http.HandlerFunc(h.deliver)))
	router.Handle("POST /errands/{errandId}/report-issue", h.guard(http.HandlerFunc(h.reportIssue)))
}

// action describes one of the courier's state changes.
type action struct {
	// operation names the action in logs.
	operation string
	event     errand.Event
	// reasoned means the body is a ReasonedAction.
	reasoned bool
	// needsDeadlines means Next needs the deadline settings (deliver sets
	// the verification deadline).
	needsDeadlines bool
	// done reports whether e already shows the effect of caller's request,
	// so a retry whose first response was lost is answered 200 without
	// writing anything.
	done func(e errand.Errand, caller string, expectedVersion int) bool
	// announce, when set, records a 409 refusal with Store.RecordRejection
	// so the courier is notified.
	announce errand.RejectedAction
}

// withdrawAction is ASSIGNED → OPEN, or ASSIGNED → EXPIRED with the
// reservation released once the acceptance deadline has passed (F3.6.1,
// F4.3.3). A withdrawal clears the courier, so its retry comes from a former
// courier: the errand is OPEN or EXPIRED one version later.
var withdrawAction = action{
	operation: "withdraw",
	event:     errand.EventWithdraw,
	done: func(e errand.Errand, _ string, expectedVersion int) bool {
		return (e.Status == errand.StatusOpen || e.Status == errand.StatusExpired) &&
			e.Version == expectedVersion+1
	},
}

// pickUpAction is ASSIGNED → PICKED_UP (F3.8.1). A refusal is announced:
// the usual cause is the pickup deadline withdrawing the courier meanwhile.
var pickUpAction = action{
	operation: "pickup",
	event:     errand.EventPickUp,
	done: func(e errand.Errand, caller string, expectedVersion int) bool {
		return e.Status == errand.StatusPickedUp && e.CourierID == caller && e.Version == expectedVersion+1
	},
	announce: errand.RejectedPickup,
}

// deliverAction is PICKED_UP → DELIVERED with the verification deadline
// set (F3.8.2, F3.10.3). Only a deliver reaches DELIVERED.
var deliverAction = action{
	operation:      "deliver",
	event:          errand.EventDeliver,
	needsDeadlines: true,
	done: func(e errand.Errand, caller string, expectedVersion int) bool {
		return e.Status == errand.StatusDelivered && e.CourierID == caller && e.Version == expectedVersion+1
	},
}

// reportIssueAction is PICKED_UP → REVIEW_REQUIRED with COURIER_ISSUE
// (F3.6.2). Only the courier's report sets that reason.
var reportIssueAction = action{
	operation: "report issue",
	event:     errand.EventReportIssue,
	reasoned:  true,
	done: func(e errand.Errand, caller string, expectedVersion int) bool {
		return e.Status == errand.StatusReviewRequired &&
			e.ReviewReason == errand.ReviewCourierIssue &&
			e.CourierID == caller &&
			e.Version == expectedVersion+1
	},
}

func (h Handler) withdraw(response http.ResponseWriter, request *http.Request) {
	h.transition(response, request, withdrawAction)
}

func (h Handler) pickUp(response http.ResponseWriter, request *http.Request) {
	h.transition(response, request, pickUpAction)
}

func (h Handler) deliver(response http.ResponseWriter, request *http.Request) {
	h.transition(response, request, deliverAction)
}

func (h Handler) reportIssue(response http.ResponseWriter, request *http.Request) {
	h.transition(response, request, reportIssueAction)
}

// transition runs a: it reads the body and the errand, checks that the
// caller is (or was) its courier, asks errand.Next, checks expectedVersion
// and persists the transition with Store.Apply.
func (h Handler) transition(response http.ResponseWriter, request *http.Request, a action) {
	ctx := request.Context()
	caller, ok := errandhttp.Caller(response, request)
	if !ok {
		return
	}
	id, err := errandhttp.ErrandID(request)
	if err != nil {
		h.fail(response, request, a.operation, "", err)
		return
	}
	var expectedVersion int
	var reason string
	if a.reasoned {
		expectedVersion, reason, err = errandhttp.ReadReasonedAction(response, request)
	} else {
		expectedVersion, err = errandhttp.ReadVersionedAction(response, request)
	}
	if err != nil {
		h.fail(response, request, a.operation, id, err)
		return
	}

	e, err := h.store.Get(ctx, id)
	if err != nil {
		h.fail(response, request, a.operation, id, err)
		return
	}
	now := h.clock.Now()
	if e.CourierID != caller {
		h.notCourier(response, request, a, e, caller, expectedVersion, now)
		return
	}
	if a.done(e, caller, expectedVersion) {
		h.respondLoaded(response, request, a.operation, e)
		return
	}

	var input errand.EventInput
	input.Reason = reason
	if a.needsDeadlines {
		settings, err := h.settings.Get(ctx)
		if err != nil {
			h.fail(response, request, a.operation, id, err)
			return
		}
		input.Deadlines = settings.Deadlines
	}
	actor := errand.Actor{Type: errand.ActorCourier, ID: caller}
	t, err := errand.Next(e, a.event, actor, input, now)
	if err != nil {
		h.refuse(response, request, a, id, caller, expectedVersion, now, err)
		return
	}
	// Checked after Next, so an errand that has moved on is reported as
	// ERRAND_UNAVAILABLE rather than as a stale version.
	if e.Version != expectedVersion {
		h.refuse(response, request, a, id, caller, expectedVersion, now,
			&apperror.Error{Code: apperror.ErrandChanged, Message: "the errand has changed; reload it and try again"})
		return
	}
	// Read before the write, so a supplier-service failure leaves the errand
	// unchanged instead of answering 503 for a committed change.
	pickups, err := errandhttp.LoadPickups(ctx, h.suppliers, e)
	if err != nil {
		h.fail(response, request, a.operation, id, err)
		return
	}
	stored, err := h.store.Apply(ctx, t)
	if err != nil {
		h.refuse(response, request, a, id, caller, expectedVersion, now, err)
		return
	}
	errandhttp.WriteJSON(response, http.StatusOK, errandhttp.CourierView(stored, pickups))
}

// notCourier answers a caller who is not e's courier. Someone who never was
// gets FORBIDDEN whatever the state, so the state of another courier's
// errand is never revealed and a stranger cannot trigger a rejection event.
// A former courier (they withdrew, or the pickup deadline withdrew them) is
// told the errand has moved on: ERRAND_UNAVAILABLE, announced for pickup
// (docs/contracts.md, "Rejection events"). A withdrawal's own retry is
// answered 200.
func (h Handler) notCourier(response http.ResponseWriter, request *http.Request, a action, e errand.Errand, caller string, expectedVersion int, now time.Time) {
	former, err := h.formerCourier(request.Context(), e.ID, caller)
	if err != nil {
		h.fail(response, request, a.operation, e.ID, err)
		return
	}
	if !former {
		h.fail(response, request, a.operation, e.ID,
			&apperror.Error{Code: apperror.Forbidden, Message: "only the errand's assigned courier may do this"})
		return
	}
	if a.done(e, caller, expectedVersion) {
		h.respondLoaded(response, request, a.operation, e)
		return
	}
	h.refuse(response, request, a, e.ID, caller, expectedVersion, now,
		&apperror.Error{Code: apperror.ErrandUnavailable, Message: "you are no longer this errand's courier"})
}

// formerCourier reports whether courier was withdrawn from errandID,
// voluntarily or by the pickup deadline. A courier who withdrew may never
// accept the errand again (F3.6.3), so this is the only way to have been its
// courier and not be now.
func (h Handler) formerCourier(ctx context.Context, errandID, courier string) (bool, error) {
	history, err := h.store.CourierHistory(ctx, errandID, courier, time.Time{})
	if err != nil {
		return false, err
	}
	return history.WithdrewFromErrand, nil
}

// refuse writes err. For an announced action, a 409 from a state check
// (ERRAND_CHANGED, ERRAND_UNAVAILABLE or INVALID_TRANSITION) also records a
// rejection event so the courier is notified.
func (h Handler) refuse(response http.ResponseWriter, request *http.Request, a action, errandID, caller string, expectedVersion int, now time.Time, err error) {
	var appErr *apperror.Error
	if a.announce != "" && errors.As(err, &appErr) && isStateConflict(appErr.Code) {
		rejection := errand.Rejection{
			ErrandID:  errandID,
			Action:    a.announce,
			Actor:     errand.Actor{Type: errand.ActorCourier, ID: caller},
			Code:      appErr.Code,
			RequestID: errandhttp.RejectionRequestID(caller, expectedVersion),
			At:        now,
		}
		// Best effort, as in A4: the courier gets the 409 either way.
		if recordErr := h.store.RecordRejection(request.Context(), rejection); recordErr != nil {
			h.logger.ErrorContext(request.Context(), "recording a "+a.operation+" rejection failed",
				slog.String("errand_id", errandID), slog.Any("error", recordErr))
		}
	}
	h.fail(response, request, a.operation, errandID, err)
}

func isStateConflict(code apperror.Code) bool {
	switch code {
	case apperror.ErrandChanged, apperror.ErrandUnavailable, apperror.InvalidTransition:
		return true
	default:
		return false
	}
}

// respondLoaded writes CourierErrandView, reading the supplier versions.
func (h Handler) respondLoaded(response http.ResponseWriter, request *http.Request, operation string, e errand.Errand) {
	pickups, err := errandhttp.LoadPickups(request.Context(), h.suppliers, e)
	if err != nil {
		h.fail(response, request, operation, e.ID, err)
		return
	}
	errandhttp.WriteJSON(response, http.StatusOK, errandhttp.CourierView(e, pickups))
}

func (h Handler) fail(response http.ResponseWriter, request *http.Request, operation, errandID string, err error) {
	errandhttp.Fail(response, request, h.logger, operation, errandID, err)
}
