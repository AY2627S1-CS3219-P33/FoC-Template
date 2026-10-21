// Package create serves POST /errands (A2): it validates the submission,
// resolves the pickup supplier and delivery building against
// supplier-service, and stores the errand in PENDING_RESERVATION with its
// credit.reserve row through errand.Store.Create. Credit-service answers
// asynchronously; the errand becomes OPEN or REJECTED later (F3.1.4).
package create

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
		panic("create: store is required")
	case deps.Suppliers == nil:
		panic("create: supplier gate is required")
	case deps.Clock == nil:
		panic("create: clock is required")
	case deps.Logger == nil:
		panic("create: logger is required")
	case guard == nil:
		panic("create: auth middleware is required")
	}
	return Handler{
		store:     deps.Store,
		suppliers: deps.Suppliers,
		clock:     deps.Clock,
		guard:     guard.RequireUse,
		logger:    deps.Logger,
	}
}

// RegisterRoutes registers POST /errands behind auth.Middleware.RequireUse
// (a user token with errands:use).
func (h Handler) RegisterRoutes(router *http.ServeMux) {
	router.Handle("POST /errands", h.guard(http.HandlerFunc(h.create)))
}

// create answers 202 with the new errand, or 200 with the errand an earlier
// request with the same Idempotency-Key created (NFR7.3).
func (h Handler) create(response http.ResponseWriter, request *http.Request) {
	ctx := request.Context()
	requester, ok := errandhttp.Caller(response, request)
	if !ok {
		return
	}
	key, ok := idempotencyKey(response, request)
	if !ok {
		return
	}
	id := errandID(requester, key)

	// A replay is answered before the body is read, so a different or
	// invalid body, or a supplier that has since closed, changes nothing.
	existing, err := h.store.Get(ctx, id)
	switch {
	case err == nil:
		if existing.RequesterID != requester || existing.CreateIdempotencyKey != key {
			h.fail(response, request, id, fmt.Errorf("create: errand %s does not belong to this request", id))
			return
		}
		h.respond(response, request, existing, nil, http.StatusOK)
		return
	case !hasCode(err, apperror.ErrandNotFound):
		h.fail(response, request, id, err)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(response, request.Body, errandhttp.MaxErrandBodyBytes))
	if err != nil {
		var sizeErr *http.MaxBytesError
		if errors.As(err, &sizeErr) {
			errandhttp.WriteError(response, apperror.InvalidArgument, "request body is too large")
			return
		}
		errandhttp.WriteError(response, apperror.InvalidArgument, "request body could not be read")
		return
	}
	now := h.clock.Now()
	in, err := parseInput(body, now)
	if err != nil {
		h.fail(response, request, id, err)
		return
	}
	building, err := errandhttp.DeliveryBuilding(ctx, h.suppliers, in.DeliveryBuildingID)
	if err != nil {
		h.fail(response, request, id, err)
		return
	}
	// Live and never cached (F2.5.2). Store.Create's fence check closes the
	// race with a deletion that starts after this call.
	version, err := h.suppliers.ResolveForCreate(ctx, in.SupplierID, now)
	if err != nil {
		h.fail(response, request, id, err)
		return
	}
	snapshot, err := json.Marshal(building)
	if err != nil {
		h.fail(response, request, id, fmt.Errorf("create: encode building snapshot: %w", err))
		return
	}

	e := errand.Errand{
		ID:          id,
		RequesterID: requester,
		Details: errand.Details{
			SupplierID:               in.SupplierID,
			SupplierVersionID:        version.VersionID,
			DeliveryBuildingID:       in.DeliveryBuildingID,
			DeliveryBuildingSnapshot: snapshot,
			DeliveryDetail:           in.DeliveryDetail,
			Items:                    in.Items,
			Notes:                    in.Notes,
			ContactName:              in.ContactName,
			ContactMobile:            in.ContactMobile,
			Reward:                   in.Reward,
			AcceptanceDeadline:       in.AcceptanceDeadline,
		},
		CreateIdempotencyKey: key,
	}
	actor := errand.Actor{Type: errand.ActorRequester, ID: requester}
	t, err := errand.Next(e, errand.EventSubmit, actor, errand.EventInput{}, now)
	if err != nil {
		h.fail(response, request, id, err)
		return
	}
	stored, created, err := h.store.Create(ctx, e, t)
	if err != nil {
		h.fail(response, request, id, err)
		return
	}
	if !created {
		// A concurrent request with the same key committed first.
		h.respond(response, request, stored, nil, http.StatusOK)
		return
	}
	h.respond(response, request, stored, errandhttp.Pickups{version.VersionID: version}, http.StatusAccepted)
}

// respond writes RequesterErrandView. pickups may be nil, in which case the
// supplier versions are read from supplier-service.
func (h Handler) respond(response http.ResponseWriter, request *http.Request, e errand.Errand, pickups errandhttp.Pickups, status int) {
	if pickups == nil {
		var err error
		if pickups, err = errandhttp.LoadPickups(request.Context(), h.suppliers, e); err != nil {
			h.fail(response, request, e.ID, err)
			return
		}
	}
	if status == http.StatusAccepted {
		response.Header().Set("Location", "/errands/"+e.ID)
	}
	errandhttp.WriteJSON(response, status, errandhttp.RequesterView(e, pickups))
}

// idempotencyKey reads the required Idempotency-Key header, a UUID.
func idempotencyKey(response http.ResponseWriter, request *http.Request) (string, bool) {
	key := request.Header.Get("Idempotency-Key")
	switch {
	case key == "":
		errandhttp.WriteError(response, apperror.InvalidArgument, "Idempotency-Key header is required")
		return "", false
	case !errandhttp.UUIDPattern.MatchString(key):
		errandhttp.WriteError(response, apperror.InvalidArgument, "Idempotency-Key must be a UUID")
		return "", false
	}
	return strings.ToLower(key), true
}

func (h Handler) fail(response http.ResponseWriter, request *http.Request, errandID string, err error) {
	errandhttp.Fail(response, request, h.logger, "create", errandID, err)
}

func hasCode(err error, code apperror.Code) bool {
	var appErr *apperror.Error
	return errors.As(err, &appErr) && appErr.Code == code
}
