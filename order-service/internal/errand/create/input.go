package create

import (
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand/errandhttp"
)

// Input is a validated ErrandCreate body. UUIDs are lower case, Items is
// re-encoded as a JSON array of Item, and AcceptanceDeadline is resolved
// (the default when the body has none).
type Input struct {
	SupplierID         string
	DeliveryBuildingID string
	DeliveryDetail     string
	Items              json.RawMessage
	Notes              string
	ContactName        string
	ContactMobile      string
	Reward             int
	AcceptanceDeadline time.Time
}

// parseInput validates an ErrandCreate body at now. Every invalid field is
// reported in one VALIDATION_FAILED (NFR2.2.1); a body that is not a single
// JSON object is INVALID_ARGUMENT. Whether the supplier and building exist is
// checked later, against supplier-service.
func parseInput(body []byte, now time.Time) (Input, error) {
	raw, err := errandhttp.DecodeObject(body)
	if err != nil {
		return Input{}, err
	}
	v := errandhttp.NewValidator(raw)
	in := Input{
		SupplierID:         v.UUID("supplierId", true),
		DeliveryBuildingID: v.UUID("deliveryBuildingId", true),
		DeliveryDetail:     v.Text("deliveryDetail", true, errandhttp.DeliveryDetailText),
		Items:              v.Items("items", true),
		Notes:              v.Text("notes", false, errandhttp.NotesText),
		ContactName:        v.Text("contactName", true, errandhttp.ContactNameText),
		ContactMobile:      v.Text("contactMobile", true, errandhttp.ContactMobileText),
		Reward:             v.Integer("reward", true, 1, math.MaxInt32, "must be a positive integer"),
		AcceptanceDeadline: acceptanceDeadline(v, "acceptanceDeadline", now),
	}
	v.OnlyKnown("supplierId", "deliveryBuildingId", "deliveryDetail", "items", "notes",
		"contactName", "contactMobile", "reward", "acceptanceDeadline")
	if err := v.Err("errand is invalid"); err != nil {
		return Input{}, err
	}
	return in, nil
}

// acceptanceDeadline reads the optional deadline and applies F3.1.3's
// bounds, returning the default when it is absent.
func acceptanceDeadline(v *errandhttp.Validator, name string, now time.Time) time.Time {
	requested, ok := v.DateTime(name, false)
	if !ok {
		return time.Time{}
	}
	deadline, err := errand.AcceptanceDeadline(requested, now)
	if err != nil {
		var appErr *apperror.Error
		if errors.As(err, &appErr) {
			v.Add(appErr.Fields...)
		} else {
			v.Fail(name, "is invalid")
		}
		return time.Time{}
	}
	return deadline
}
