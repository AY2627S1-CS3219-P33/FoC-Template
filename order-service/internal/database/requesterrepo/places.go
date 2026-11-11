// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-09
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

package requesterrepo

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/requestergen"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/places"
)

// Unique indexes from migrations/00002_create_saved_places.sql.
const (
	labelIndex    = "saved_places_label_idx"
	supplierIndex = "saved_places_supplier_idx"
)

func (r *Repository) List(ctx context.Context, userID string, kind places.Kind) ([]places.Place, error) {
	rows, err := r.queries.ListPlaces(ctx, requestergen.ListPlacesParams{
		UserID: userID,
		Kind:   text(string(kind)),
	})
	if err != nil {
		return nil, err
	}
	out := make([]places.Place, 0, len(rows))
	for _, row := range rows {
		out = append(out, placeFromRow(row))
	}
	return out, nil
}

func (r *Repository) Create(ctx context.Context, p places.NewPlace) (places.Place, error) {
	supplierID, err := parseUUID("supplierId", p.SupplierID)
	if err != nil {
		return places.Place{}, err
	}
	buildingID, err := parseUUID("deliveryBuildingId", p.DeliveryBuildingID)
	if err != nil {
		return places.Place{}, err
	}

	var stored places.Place
	err = r.inTx(ctx, func(q *requestergen.Queries) error {
		if err := q.LockUserPlaces(ctx, p.UserID); err != nil {
			return err
		}
		count, err := q.CountPlaces(ctx, requestergen.CountPlacesParams{UserID: p.UserID, Kind: string(p.Kind)})
		if err != nil {
			return err
		}
		if count >= places.MaxPerKind {
			return places.LimitReached(p.Kind)
		}
		row, err := q.InsertPlace(ctx, requestergen.InsertPlaceParams{
			UserID:             p.UserID,
			Kind:               string(p.Kind),
			Label:              p.Label,
			SupplierID:         supplierID,
			DeliveryBuildingID: buildingID,
			DeliveryDetail:     text(p.DeliveryDetail),
		})
		if err != nil {
			return mapUnique(err)
		}
		stored = placeFromRow(row)
		return nil
	})
	return stored, err
}

func (r *Repository) Update(ctx context.Context, userID, placeID string, patch places.PlacePatch) (places.Place, error) {
	id, ok := toUUID(placeID)
	if !ok {
		return places.Place{}, places.NotFound()
	}
	current, err := r.queries.GetPlace(ctx, requestergen.GetPlaceParams{ID: id, UserID: userID})
	if err != nil {
		return places.Place{}, mapNotFound(err)
	}
	if patch.DeliveryDetail != nil && places.Kind(current.Kind) != places.KindDelivery {
		return places.Place{}, places.DetailOnPickup()
	}
	if patch.Label == nil && patch.DeliveryDetail == nil {
		return placeFromRow(current), nil
	}
	row, err := r.queries.UpdatePlace(ctx, requestergen.UpdatePlaceParams{
		Label:          textPtr(patch.Label),
		DeliveryDetail: textPtr(patch.DeliveryDetail),
		ID:             id,
		UserID:         userID,
	})
	if err != nil {
		// A concurrent Delete between the two statements is not found.
		return places.Place{}, mapUnique(mapNotFound(err))
	}
	return placeFromRow(row), nil
}

func (r *Repository) Delete(ctx context.Context, userID, placeID string) error {
	id, ok := toUUID(placeID)
	if !ok {
		return places.NotFound()
	}
	deleted, err := r.queries.DeletePlace(ctx, requestergen.DeletePlaceParams{ID: id, UserID: userID})
	if err != nil {
		return err
	}
	if deleted == 0 {
		return places.NotFound()
	}
	return nil
}

func mapNotFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return places.NotFound()
	}
	return err
}

// mapUnique turns a violation of a saved_places unique index into its
// field-level VALIDATION_FAILED.
func mapUnique(err error) error {
	switch uniqueViolation(err) {
	case labelIndex:
		return places.DuplicateLabel()
	case supplierIndex:
		return places.DuplicateSupplier()
	}
	return err
}

func placeFromRow(row requestergen.SavedPlace) places.Place {
	return places.Place{
		ID:                 uuidString(row.ID),
		UserID:             row.UserID,
		Kind:               places.Kind(row.Kind),
		Label:              row.Label,
		SupplierID:         uuidString(row.SupplierID),
		DeliveryBuildingID: uuidString(row.DeliveryBuildingID),
		DeliveryDetail:     row.DeliveryDetail.String,
		CreatedAt:          timeOrZero(row.CreatedAt),
		UpdatedAt:          timeOrZero(row.UpdatedAt),
	}
}
