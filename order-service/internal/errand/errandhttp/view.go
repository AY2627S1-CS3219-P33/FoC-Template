package errandhttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

// Pickup is PickupLocation: the pickup location from a supplier version.
type Pickup struct {
	Name                string  `json:"name"`
	Building            string  `json:"building"`
	Floor               string  `json:"floor"`
	LocationDescription string  `json:"locationDescription"`
	Latitude            float64 `json:"latitude"`
	Longitude           float64 `json:"longitude"`
}

func PickupOf(v errand.SupplierVersion) Pickup {
	return Pickup{
		Name:                v.Name,
		Building:            v.Building,
		Floor:               v.Floor,
		LocationDescription: v.LocationDescription,
		Latitude:            v.Latitude,
		Longitude:           v.Longitude,
	}
}

// Building is DeliveryBuilding: only the id and name of the stored snapshot.
type Building struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func BuildingOf(d errand.Details) Building {
	var snapshot struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(d.DeliveryBuildingSnapshot, &snapshot)
	return Building{ID: d.DeliveryBuildingID, Name: snapshot.Name}
}

// Fields is ErrandFields, the part every errand view shares.
type Fields struct {
	ErrandID             string              `json:"errandId"`
	Version              int                 `json:"version"`
	Status               errand.Status       `json:"status"`
	SupplierID           string              `json:"supplierId"`
	SupplierVersionID    string              `json:"supplierVersionId"`
	Pickup               Pickup              `json:"pickup"`
	DeliveryBuilding     Building            `json:"deliveryBuilding"`
	DeliveryDetail       string              `json:"deliveryDetail"`
	Items                json.RawMessage     `json:"items"`
	Notes                string              `json:"notes"`
	Reward               int                 `json:"reward"`
	CreatedAt            time.Time           `json:"createdAt"`
	UpdatedAt            time.Time           `json:"updatedAt"`
	AcceptanceDeadline   time.Time           `json:"acceptanceDeadline"`
	PickupDeadline       *time.Time          `json:"pickupDeadline,omitempty"`
	DeliveryDeadline     *time.Time          `json:"deliveryDeadline,omitempty"`
	DeliveredAt          *time.Time          `json:"deliveredAt,omitempty"`
	VerificationDeadline *time.Time          `json:"verificationDeadline,omitempty"`
	ReviewReason         errand.ReviewReason `json:"reviewReason,omitempty"`
}

// FieldsOf builds the shared fields; pickup is e's supplier version.
func FieldsOf(e errand.Errand, pickup errand.SupplierVersion) Fields {
	return Fields{
		ErrandID:             e.ID,
		Version:              e.Version,
		Status:               e.Status,
		SupplierID:           e.Details.SupplierID,
		SupplierVersionID:    e.Details.SupplierVersionID,
		Pickup:               PickupOf(pickup),
		DeliveryBuilding:     BuildingOf(e.Details),
		DeliveryDetail:       e.Details.DeliveryDetail,
		Items:                e.Details.Items,
		Notes:                e.Details.Notes,
		Reward:               e.Details.Reward,
		CreatedAt:            e.CreatedAt,
		UpdatedAt:            e.UpdatedAt,
		AcceptanceDeadline:   e.Details.AcceptanceDeadline,
		PickupDeadline:       e.PickupDeadline,
		DeliveryDeadline:     e.DeliveryDeadline,
		DeliveredAt:          e.DeliveredAt,
		VerificationDeadline: e.VerificationDeadline,
		ReviewReason:         e.ReviewReason,
	}
}

// RequesterErrand is RequesterErrandView: the errand as its requester sees
// it, with the contact person they chose and nothing about the courier
// (NFR3.6.4).
//
// rejectionReason is not returned: the Store port does not expose the
// REJECTED transition's reason, and the field is optional.
type RequesterErrand struct {
	Fields
	ViewerRole    string       `json:"viewerRole"`
	ContactName   string       `json:"contactName"`
	ContactMobile string       `json:"contactMobile"`
	PendingEdit   *PendingEdit `json:"pendingEdit,omitempty"`
}

// PendingEdit is the PendingEdit schema: a reward edit waiting for
// credit-service.
type PendingEdit struct {
	SupplierID         string          `json:"supplierId"`
	SupplierVersionID  string          `json:"supplierVersionId"`
	Pickup             Pickup          `json:"pickup"`
	DeliveryBuilding   Building        `json:"deliveryBuilding"`
	DeliveryDetail     string          `json:"deliveryDetail"`
	Items              json.RawMessage `json:"items"`
	Notes              string          `json:"notes"`
	ContactName        string          `json:"contactName"`
	ContactMobile      string          `json:"contactMobile"`
	Reward             int             `json:"reward"`
	AcceptanceDeadline time.Time       `json:"acceptanceDeadline"`
	RequestedAt        time.Time       `json:"requestedAt"`
}

// Pickups maps supplier version IDs to versions, as LoadPickups returns them.
type Pickups map[string]errand.SupplierVersion

// LoadPickups reads the supplier versions a view of e needs: e's own and its
// pending edit's. Versions are immutable, so the gate may serve them from a
// cache (F2.4.3, F2.5.1).
//
// A version supplier-service does not know is a plain error, not
// SUPPLIER_UNAVAILABLE: versions are kept for deleted suppliers, so a stored
// errand's version always exists, and a 422 would blame the client.
func LoadPickups(ctx context.Context, suppliers errand.SupplierGate, e errand.Errand) (Pickups, error) {
	ids := []string{e.Details.SupplierVersionID}
	if e.PendingEdit != nil {
		ids = append(ids, e.PendingEdit.Details.SupplierVersionID)
	}
	pickups := Pickups{}
	for _, id := range ids {
		if _, ok := pickups[id]; ok {
			continue
		}
		v, err := suppliers.GetVersion(ctx, id)
		if err != nil {
			var appErr *apperror.Error
			if errors.As(err, &appErr) && appErr.Code == apperror.SupplierUnavailable {
				// %v, not %w: the code must not reach the client.
				return nil, fmt.Errorf("errandhttp: errand %s has an unknown supplier version %s: %v", e.ID, id, err)
			}
			return nil, err
		}
		pickups[id] = v
	}
	return pickups, nil
}

// RequesterView builds RequesterErrandView. pickups must hold every version
// LoadPickups would load for e.
func RequesterView(e errand.Errand, pickups Pickups) RequesterErrand {
	view := RequesterErrand{
		Fields:        FieldsOf(e, pickups[e.Details.SupplierVersionID]),
		ViewerRole:    string(errand.ActorRequester),
		ContactName:   e.Details.ContactName,
		ContactMobile: e.Details.ContactMobile,
	}
	if p := e.PendingEdit; p != nil {
		d := p.Details
		view.PendingEdit = &PendingEdit{
			SupplierID:         d.SupplierID,
			SupplierVersionID:  d.SupplierVersionID,
			Pickup:             PickupOf(pickups[d.SupplierVersionID]),
			DeliveryBuilding:   BuildingOf(d),
			DeliveryDetail:     d.DeliveryDetail,
			Items:              d.Items,
			Notes:              d.Notes,
			ContactName:        d.ContactName,
			ContactMobile:      d.ContactMobile,
			Reward:             d.Reward,
			AcceptanceDeadline: d.AcceptanceDeadline,
			RequestedAt:        p.At,
		}
	}
	return view
}

// OpenViewOf is the redacted open-list projection of e (errand.OpenErrandView),
// the only errand shape that may be cached. The delivery building is
// projected to its id and name, as DeliveryBuilding allows.
func OpenViewOf(e errand.Errand, pickup errand.SupplierVersion) errand.OpenErrandView {
	building, _ := json.Marshal(BuildingOf(e.Details))
	return errand.OpenErrandView{
		ErrandID:           e.ID,
		Version:            e.Version,
		SupplierID:         e.Details.SupplierID,
		SupplierVersionID:  e.Details.SupplierVersionID,
		Pickup:             errand.OpenPickupView(PickupOf(pickup)),
		DeliveryBuilding:   building,
		DeliveryDetail:     e.Details.DeliveryDetail,
		Items:              e.Details.Items,
		Reward:             e.Details.Reward,
		CreatedAt:          e.CreatedAt,
		AcceptanceDeadline: e.Details.AcceptanceDeadline,
	}
}

// OpenFields is OpenErrandFields: what any user may see of an open errand.
// It has no requester identity, no notes and no contact person (NFR3.6.4).
type OpenFields struct {
	ErrandID           string          `json:"errandId"`
	Version            int             `json:"version"`
	SupplierID         string          `json:"supplierId"`
	SupplierVersionID  string          `json:"supplierVersionId"`
	Pickup             Pickup          `json:"pickup"`
	DeliveryBuilding   Building        `json:"deliveryBuilding"`
	DeliveryDetail     string          `json:"deliveryDetail"`
	Items              json.RawMessage `json:"items"`
	Reward             int             `json:"reward"`
	CreatedAt          time.Time       `json:"createdAt"`
	AcceptanceDeadline time.Time       `json:"acceptanceDeadline"`
}

func OpenFieldsOf(v errand.OpenErrandView) OpenFields {
	var building Building
	_ = json.Unmarshal(v.DeliveryBuilding, &building)
	return OpenFields{
		ErrandID:           v.ErrandID,
		Version:            v.Version,
		SupplierID:         v.SupplierID,
		SupplierVersionID:  v.SupplierVersionID,
		Pickup:             Pickup(v.Pickup),
		DeliveryBuilding:   building,
		DeliveryDetail:     v.DeliveryDetail,
		Items:              v.Items,
		Reward:             v.Reward,
		CreatedAt:          v.CreatedAt,
		AcceptanceDeadline: v.AcceptanceDeadline,
	}
}

// PublicErrand is PublicErrandView: GET /errands/{id} for a caller who is
// neither the requester nor the assigned courier, while the errand is OPEN.
type PublicErrand struct {
	OpenFields
	ViewerRole string `json:"viewerRole"`
}

// ViewerPublic is PublicErrandView's viewerRole.
const ViewerPublic = "PUBLIC"

func PublicView(e errand.Errand, pickups Pickups) PublicErrand {
	return PublicErrand{
		OpenFields: OpenFieldsOf(OpenViewOf(e, pickups[e.Details.SupplierVersionID])),
		ViewerRole: ViewerPublic,
	}
}

// CourierErrand is CourierErrandView: the errand as its assigned courier sees
// it, with the requester's notes (F3.12) but never the requester's identity.
type CourierErrand struct {
	Fields
	ViewerRole    string `json:"viewerRole"`
	ContactName   string `json:"contactName,omitempty"`
	ContactMobile string `json:"contactMobile,omitempty"`
}

// contactStatuses are the states in which the courier sees the contact
// person (NFR3.6.4).
var contactStatuses = []errand.Status{
	errand.StatusAssigned, errand.StatusPickedUp, errand.StatusDelivered, errand.StatusReviewRequired,
}

func CourierView(e errand.Errand, pickups Pickups) CourierErrand {
	view := CourierErrand{
		Fields:     FieldsOf(e, pickups[e.Details.SupplierVersionID]),
		ViewerRole: string(errand.ActorCourier),
	}
	if slices.Contains(contactStatuses, e.Status) {
		view.ContactName = e.Details.ContactName
		view.ContactMobile = e.Details.ContactMobile
	}
	return view
}
