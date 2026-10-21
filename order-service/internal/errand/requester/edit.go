package requester

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand/errandhttp"
)

// editInput is a validated ErrandEdit body. A nil field was omitted (or
// null) and keeps its current value.
type editInput struct {
	expectedVersion    int
	supplierID         *string
	deliveryBuildingID *string
	deliveryDetail     *string
	items              json.RawMessage
	notes              *string
	contactName        *string
	contactMobile      *string
	reward             *int
	acceptanceDeadline *time.Time
}

// editableFields are ErrandEdit's members besides expectedVersion.
var editableFields = []string{
	"supplierId", "deliveryBuildingId", "deliveryDetail", "items", "notes",
	"contactName", "contactMobile", "reward", "acceptanceDeadline",
}

// parseEdit validates an ErrandEdit body. Field formats are checked here, as
// on creation; rules that depend on the errand (the acceptance-deadline
// window, a deadline that has passed) are errand.Next's.
func parseEdit(raw map[string]json.RawMessage) (editInput, error) {
	v := errandhttp.NewValidator(raw)
	text := func(name string, rule errandhttp.TextRule) *string {
		if !v.Has(name) {
			return nil
		}
		s := v.Text(name, true, rule)
		return &s
	}
	uuid := func(name string) *string {
		if !v.Has(name) {
			return nil
		}
		s := v.UUID(name, true)
		return &s
	}
	in := editInput{
		expectedVersion:    v.ExpectedVersion(),
		supplierID:         uuid("supplierId"),
		deliveryBuildingID: uuid("deliveryBuildingId"),
		deliveryDetail:     text("deliveryDetail", errandhttp.DeliveryDetailText),
		items:              v.Items("items", false),
		notes:              text("notes", errandhttp.NotesText),
		contactName:        text("contactName", errandhttp.ContactNameText),
		contactMobile:      text("contactMobile", errandhttp.ContactMobileText),
	}
	if v.Has("reward") {
		reward := v.Integer("reward", true, 1, math.MaxInt32, "must be a positive integer")
		in.reward = &reward
	}
	in.acceptanceDeadline, _ = v.DateTime("acceptanceDeadline", false)
	v.OnlyKnown(append([]string{"expectedVersion"}, editableFields...)...)
	if err := v.Err("errand is invalid"); err != nil {
		return editInput{}, err
	}
	changes := false
	for _, name := range editableFields {
		changes = changes || v.Has(name)
	}
	if !changes {
		return editInput{}, &apperror.Error{Code: apperror.InvalidArgument, Message: "request body must change at least one field"}
	}
	return in, nil
}

// applyTo returns d with the edit's fields replaced. A changed supplier or
// building keeps d's version and snapshot until resolve replaces them.
func (in editInput) applyTo(d errand.Details) errand.Details {
	set := func(field *string, value *string) {
		if value != nil {
			*field = *value
		}
	}
	set(&d.SupplierID, in.supplierID)
	set(&d.DeliveryBuildingID, in.deliveryBuildingID)
	set(&d.DeliveryDetail, in.deliveryDetail)
	set(&d.Notes, in.notes)
	set(&d.ContactName, in.contactName)
	set(&d.ContactMobile, in.contactMobile)
	if in.items != nil {
		d.Items = in.items
	}
	if in.reward != nil {
		d.Reward = *in.reward
	}
	if in.acceptanceDeadline != nil {
		d.AcceptanceDeadline = *in.acceptanceDeadline
	}
	return d
}

// edit answers 200 when the edit is applied at once, or 202 when it changes
// the reward and waits for credit-service as the errand's pending edit
// (F4.4.3). Edit refusals are not announced: only accept, cancel and pickup
// have rejection events.
func (h Handler) edit(response http.ResponseWriter, request *http.Request) {
	const operation = "edit"
	ctx := request.Context()
	caller, ok := errandhttp.Caller(response, request)
	if !ok {
		return
	}
	id, err := errandhttp.ErrandID(request)
	if err != nil {
		h.fail(response, request, operation, "", err)
		return
	}
	raw, err := errandhttp.ReadObject(response, request, errandhttp.MaxErrandBodyBytes)
	if err != nil {
		h.fail(response, request, operation, id, err)
		return
	}
	in, err := parseEdit(raw)
	if err != nil {
		h.fail(response, request, operation, id, err)
		return
	}

	e, err := h.ownErrand(request, id, caller)
	if err != nil {
		h.fail(response, request, operation, id, err)
		return
	}
	now := h.clock.Now()
	actor := errand.Actor{Type: errand.ActorRequester, ID: caller}
	edited := in.applyTo(e.Details)
	// Decided once before supplier-service is called, so an edit refused by
	// the errand's state, a pending edit, a stale version or an invalid
	// field costs no remote call.
	if _, err := errand.Next(e, errand.EventEdit, actor, errand.EventInput{Edit: &edited}, now); err != nil {
		h.fail(response, request, operation, id, err)
		return
	}
	if e.Version != in.expectedVersion {
		h.fail(response, request, operation, id, staleVersion())
		return
	}
	if err := h.resolve(ctx, e.Details, &edited, now); err != nil {
		h.fail(response, request, operation, id, err)
		return
	}
	t, err := errand.Next(e, errand.EventEdit, actor, errand.EventInput{Edit: &edited}, now)
	if err != nil {
		h.fail(response, request, operation, id, err)
		return
	}
	// Read before the write, as in transition.
	pickups, err := errandhttp.LoadPickups(ctx, h.suppliers, t.ApplyTo(e))
	if err != nil {
		h.fail(response, request, operation, id, err)
		return
	}
	stored, err := h.store.Apply(ctx, t)
	if err != nil {
		h.fail(response, request, operation, id, err)
		return
	}
	status := http.StatusOK
	if stored.PendingEdit != nil {
		status = http.StatusAccepted
	}
	h.respond(response, stored, pickups, status)
}

// resolve completes edited against supplier-service, as on creation: a
// changed supplier is resolved to its current version, which must be open
// now (F3.1.1, F2.5.2), and a changed building must exist, not be retired,
// and is snapshotted again (F3.1.1). An unchanged supplier keeps its stored
// version, even if the supplier has published a newer one since. Whether the
// new supplier is being deleted is checked by Store.Apply.
func (h Handler) resolve(ctx context.Context, current errand.Details, edited *errand.Details, now time.Time) error {
	if !strings.EqualFold(edited.DeliveryBuildingID, current.DeliveryBuildingID) {
		building, err := errandhttp.DeliveryBuilding(ctx, h.suppliers, edited.DeliveryBuildingID)
		if err != nil {
			return err
		}
		snapshot, err := json.Marshal(building)
		if err != nil {
			return err
		}
		edited.DeliveryBuildingSnapshot = snapshot
	}
	if !strings.EqualFold(edited.SupplierID, current.SupplierID) {
		version, err := h.suppliers.ResolveForCreate(ctx, edited.SupplierID, now)
		if err != nil {
			return err
		}
		edited.SupplierVersionID = version.VersionID
	}
	return nil
}
