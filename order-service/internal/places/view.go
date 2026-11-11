// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-09
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

package places

import (
	"context"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

// maxSupplierReads bounds the concurrent GetCurrent calls of one request.
const maxSupplierReads = 5

// item is DeliveryPlace or PickupPlace: the stored place and what
// supplier-service says about it now. Unused fields are left out.
type item struct {
	PlaceID          string           `json:"placeId"`
	Kind             Kind             `json:"kind"`
	Label            string           `json:"label"`
	SupplierID       string           `json:"supplierId,omitempty"`
	Supplier         *supplierSummary `json:"supplier,omitempty"`
	DeliveryBuilding *building        `json:"deliveryBuilding,omitempty"`
	DeliveryDetail   string           `json:"deliveryDetail,omitempty"`
	Available        bool             `json:"available"`
	CreatedAt        time.Time        `json:"createdAt"`
}

// supplierSummary is SavedSupplierSummary, from the supplier's current
// version.
type supplierSummary struct {
	Name        string `json:"name"`
	Building    string `json:"building"`
	Floor       string `json:"floor"`
	OpeningTime string `json:"openingTime"`
	ClosingTime string `json:"closingTime"`
}

// building is SavedPlaceBuilding: the building's current name and whether
// it is retired, not a snapshot.
type building struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Retired bool   `json:"retired"`
}

// lookups is what supplier-service said about the buildings and suppliers
// of some places.
type lookups struct {
	// buildings maps lower-case building IDs to buildings.
	buildings map[string]errand.Building
	// suppliers maps supplier IDs to their current version, or to nil for
	// a supplier that is missing or deleted.
	suppliers map[string]*errand.SupplierVersion
}

// resolve reads what the items of ps need: the building list once if any
// place is a DELIVERY place, and each distinct PICKUP supplier's current
// version, at most maxSupplierReads at a time. A missing or deleted supplier
// is unavailable, not an error; any other failure, such as
// DEPENDENCY_UNAVAILABLE, is returned.
func (h Handler) resolve(ctx context.Context, ps []Place) (lookups, error) {
	found := lookups{suppliers: map[string]*errand.SupplierVersion{}}
	var supplierIDs []string
	needBuildings := false
	for _, p := range ps {
		switch p.Kind {
		case KindDelivery:
			needBuildings = true
		case KindPickup:
			if _, seen := found.suppliers[p.SupplierID]; !seen {
				found.suppliers[p.SupplierID] = nil
				supplierIDs = append(supplierIDs, p.SupplierID)
			}
		}
	}

	if needBuildings {
		buildings, err := h.suppliers.ListBuildings(ctx)
		if err != nil {
			return lookups{}, err
		}
		found.buildings = make(map[string]errand.Building, len(buildings))
		for _, b := range buildings {
			found.buildings[strings.ToLower(b.ID)] = b
		}
	}

	// Each goroutine writes only its own element.
	versions := make([]*errand.SupplierVersion, len(supplierIDs))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(maxSupplierReads)
	for i, id := range supplierIDs {
		group.Go(func() error {
			version, err := h.suppliers.GetCurrent(groupCtx, id)
			switch {
			case err == nil:
				versions[i] = &version
			case !hasCode(err, apperror.SupplierUnavailable):
				return err
			}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return lookups{}, err
	}
	for i, id := range supplierIDs {
		found.suppliers[id] = versions[i]
	}
	return found, nil
}

// item renders p. A DELIVERY place is available while its building is in
// supplier-service's list and not retired; a PICKUP place while its
// supplier exists and is not deleted, whatever its opening hours.
func (l lookups) item(p Place) item {
	out := item{
		PlaceID:   p.ID,
		Kind:      p.Kind,
		Label:     p.Label,
		CreatedAt: p.CreatedAt,
	}
	switch p.Kind {
	case KindDelivery:
		b, ok := l.buildings[strings.ToLower(p.DeliveryBuildingID)]
		out.DeliveryBuilding = &building{ID: p.DeliveryBuildingID, Name: b.Name, Retired: b.Retired}
		out.DeliveryDetail = p.DeliveryDetail
		out.Available = ok && !b.Retired
	case KindPickup:
		out.SupplierID = p.SupplierID
		if v := l.suppliers[p.SupplierID]; v != nil {
			out.Supplier = &supplierSummary{
				Name:        v.Name,
				Building:    v.Building,
				Floor:       v.Floor,
				OpeningTime: v.OpeningTime,
				ClosingTime: v.ClosingTime,
			}
			out.Available = true
		}
	}
	return out
}
