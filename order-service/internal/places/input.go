// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-09
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

package places

import (
	"encoding/json"
	"net/url"
	"slices"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand/errandhttp"
)

// maxPlaceBytes bounds a PlaceCreate or PlacePatch body: a 40-character
// label and a 200-character delivery detail at up to 4 bytes per character,
// two UUIDs and JSON syntax.
const maxPlaceBytes = 4 << 10

// labelText is the Label schema in openapi/places.yaml.
var labelText = errandhttp.TextRule{Max: MaxLabelLength, NotBlank: true}

// parseKindQuery reads the list's optional kind, which must be exactly
// DELIVERY or PICKUP.
func parseKindQuery(values url.Values) (Kind, error) {
	if !values.Has("kind") {
		return "", nil
	}
	if kind := Kind(values.Get("kind")); slices.Contains(Kinds(), kind) {
		return kind, nil
	}
	return "", &apperror.Error{Code: apperror.InvalidArgument, Message: "kind must be DELIVERY or PICKUP"}
}

// parseNewPlace validates a PlaceCreate body. Which members are required,
// and allowed, depends on kind; with a missing or unknown kind only the
// label and unknown members are checked besides it. Every invalid field is
// reported in one VALIDATION_FAILED (NFR2.2.1). UserID is left for the
// caller to set.
func parseNewPlace(raw map[string]json.RawMessage) (NewPlace, error) {
	v := errandhttp.NewValidator(raw)
	p := NewPlace{
		Kind:  readKind(v, raw),
		Label: v.Text("label", true, labelText),
	}
	switch p.Kind {
	case KindDelivery:
		p.DeliveryBuildingID = v.UUID("deliveryBuildingId", true)
		p.DeliveryDetail = v.Text("deliveryDetail", true, errandhttp.DeliveryDetailText)
		v.OnlyKnown("kind", "label", "deliveryBuildingId", "deliveryDetail")
	case KindPickup:
		p.SupplierID = v.UUID("supplierId", true)
		v.OnlyKnown("kind", "label", "supplierId")
	default:
		v.OnlyKnown("kind", "label", "supplierId", "deliveryBuildingId", "deliveryDetail")
	}
	if err := v.Err("place is invalid"); err != nil {
		return NewPlace{}, err
	}
	return p, nil
}

// readKind reads the required kind, DELIVERY or PICKUP.
func readKind(v *errandhttp.Validator, raw map[string]json.RawMessage) Kind {
	if !v.Has("kind") {
		v.Fail("kind", "is required")
		return ""
	}
	var kind Kind
	if err := json.Unmarshal(raw["kind"], &kind); err != nil || !slices.Contains(Kinds(), kind) {
		v.Fail("kind", "must be DELIVERY or PICKUP")
		return ""
	}
	return kind
}

// parsePatch validates a PlacePatch body: label and deliveryDetail, at least
// one of them. Whether the place has a delivery detail is the store's check.
func parsePatch(raw map[string]json.RawMessage) (PlacePatch, error) {
	v := errandhttp.NewValidator(raw)
	var patch PlacePatch
	if v.Has("label") {
		label := v.Text("label", true, labelText)
		patch.Label = &label
	}
	if v.Has("deliveryDetail") {
		detail := v.Text("deliveryDetail", true, errandhttp.DeliveryDetailText)
		patch.DeliveryDetail = &detail
	}
	v.OnlyKnown("label", "deliveryDetail")
	if err := v.Err("place is invalid"); err != nil {
		return PlacePatch{}, err
	}
	if patch.Label == nil && patch.DeliveryDetail == nil {
		return PlacePatch{}, &apperror.Error{Code: apperror.InvalidArgument, Message: "request body must change at least one field"}
	}
	return patch, nil
}
