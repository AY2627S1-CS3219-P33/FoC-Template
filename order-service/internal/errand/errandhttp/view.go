package errandhttp

import (
	"context"
	"encoding/json"
	"time"

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
