package requester

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand/errandhttp"
)

// action describes one of the requester's state changes that take a
// VersionedAction or ReasonedAction body.
type action struct {
	// operation names the action in logs.
	operation string
	event     errand.Event
	// reasoned means the body is a ReasonedAction.
	reasoned bool
	// done reports whether e already shows the effect of this request, so
	// it is answered 200 without writing anything: a retry whose first
	// response was lost, or a repeated confirm (F3.10.2).
	done func(e errand.Errand, expectedVersion int) bool
	// announce, when set, records a 409 refusal with Store.RecordRejection
	// so the requester is notified.
	announce errand.RejectedAction
}

// cancelAction is OPEN → CANCELLED (F3.5). Only a cancel moves an errand to
// CANCELLED, by exactly one version, so CANCELLED at expectedVersion+1 is
// this request's result.
var cancelAction = action{
	operation: "cancel",
	event:     errand.EventCancel,
	done: func(e errand.Errand, expectedVersion int) bool {
		return e.Status == errand.StatusCancelled && e.Version == expectedVersion+1
	},
	announce: errand.RejectedCancel,
}

// confirmAction is DELIVERED → COMPLETED (F3.10.1). A COMPLETED errand is
// answered 200 whatever expectedVersion says, including one an admin or the
// auto-complete job completed (F3.10.2).
var confirmAction = action{
	operation: "confirm",
	event:     errand.EventConfirm,
	done: func(e errand.Errand, _ int) bool {
		return e.Status == errand.StatusCompleted
	},
}

// disputeAction is DELIVERED → REVIEW_REQUIRED (F3.10). Only the requester's
// dispute sets REQUESTER_DISPUTE, by exactly one version.
var disputeAction = action{
	operation: "dispute",
	event:     errand.EventDispute,
	reasoned:  true,
	done: func(e errand.Errand, expectedVersion int) bool {
		return e.Status == errand.StatusReviewRequired &&
			e.ReviewReason == errand.ReviewRequesterDispute &&
			e.Version == expectedVersion+1
	},
}

func (h Handler) cancel(response http.ResponseWriter, request *http.Request) {
	h.transition(response, request, cancelAction)
}

func (h Handler) confirm(response http.ResponseWriter, request *http.Request) {
	h.transition(response, request, confirmAction)
}

func (h Handler) dispute(response http.ResponseWriter, request *http.Request) {
	h.transition(response, request, disputeAction)
}

// transition runs a: it reads the body and the errand, asks errand.Next,
// checks expectedVersion and persists the transition with Store.Apply.
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

	e, err := h.ownErrand(request, id, caller)
	if err != nil {
		h.fail(response, request, a.operation, id, err)
		return
	}
	if a.done(e, expectedVersion) {
		h.respondLoaded(response, request, a.operation, e)
		return
	}

	now := h.clock.Now()
	actor := errand.Actor{Type: errand.ActorRequester, ID: caller}
	t, err := errand.Next(e, a.event, actor, errand.EventInput{Reason: reason}, now)
	if err != nil {
		h.refuse(response, request, a, id, caller, expectedVersion, now, err)
		return
	}
	if e.Version != expectedVersion {
		h.refuse(response, request, a, id, caller, expectedVersion, now, staleVersion())
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
		done := func(current errand.Errand) bool { return a.done(current, expectedVersion) }
		if current, lost := errandhttp.LostToDuplicate(ctx, h.store, id, err, done); lost {
			h.respond(response, current, pickups, http.StatusOK)
			return
		}
		h.refuse(response, request, a, id, caller, expectedVersion, now, err)
		return
	}
	h.respond(response, stored, pickups, http.StatusOK)
}

// refuse writes err. For an announced action, a 409 from a state check
// (ERRAND_CHANGED, ERRAND_UNAVAILABLE or INVALID_TRANSITION) also records a
// rejection event so the requester is notified (docs/contracts.md,
// "Rejection events").
func (h Handler) refuse(response http.ResponseWriter, request *http.Request, a action, errandID, caller string, expectedVersion int, now time.Time, err error) {
	var appErr *apperror.Error
	if a.announce != "" && errors.As(err, &appErr) && isStateConflict(appErr.Code) {
		rejection := errand.Rejection{
			ErrandID:  errandID,
			Action:    a.announce,
			Actor:     errand.Actor{Type: errand.ActorRequester, ID: caller},
			Code:      appErr.Code,
			RequestID: errandhttp.RejectionRequestID(caller, expectedVersion),
			At:        now,
		}
		// Best effort, as in A4: the requester gets the 409 either way.
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
