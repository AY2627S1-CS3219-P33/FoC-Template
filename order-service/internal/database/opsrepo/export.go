// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-09
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

package opsrepo

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/opsgen"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

// MaxExportPageSize bounds the export routes' pages and id lists.
const MaxExportPageSize = 500

// ExportedTransition is one transition event order-service published, as
// admin-service needs it. It carries no actor, requester, reason or other
// free text.
type ExportedTransition struct {
	// ID is the outbox row ID, the keyset cursor.
	ID int64
	// MessageID is the event's messageId on the wire.
	MessageID string
	// Type is the routing key, for example "errand.accepted".
	Type          string
	ErrandID      string
	ErrandVersion int
	Event         errand.Event
	From          errand.Status
	To            errand.Status
	// CourierID is empty when the event names no courier.
	CourierID  string
	Reward     int
	OccurredAt time.Time
}

// ExportedErrand is one errand row without contact details, items or
// notes. SupplierVersionID lets the caller resolve the supplier's name;
// DeliveryBuildingName is empty when the stored snapshot has none.
type ExportedErrand struct {
	ID                   string
	Status               errand.Status
	Version              int
	CourierID            string
	Reward               int
	SupplierID           string
	SupplierVersionID    string
	DeliveryBuildingID   string
	DeliveryBuildingName string
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// ExportTransitions returns up to limit transition events with outbox ID
// above afterID, oldest first. The outbox keeps every row, so paging from 0
// returns the full history.
func (r *Repository) ExportTransitions(ctx context.Context, afterID int64, limit int) ([]ExportedTransition, error) {
	if afterID < 0 {
		return nil, invalid("after must not be negative")
	}
	if err := checkExportLimit(limit); err != nil {
		return nil, err
	}
	names := errand.TransitionEventNames()
	kinds := make([]string, 0, len(names))
	for _, name := range names {
		kinds = append(kinds, name.Kind())
	}
	rows, err := r.queries.ExportTransitionEvents(ctx, opsgen.ExportTransitionEventsParams{
		AfterID: afterID, Kinds: kinds, RowLimit: int32(limit),
	})
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]ExportedTransition, 0, len(rows))
	for _, row := range rows {
		var data errand.EventData
		if err := json.Unmarshal(row.Payload, &data); err != nil {
			return nil, fmt.Errorf("opsrepo: outbox row %d has an undecodable payload: %w", row.ID, err)
		}
		out = append(out, ExportedTransition{
			ID:            row.ID,
			MessageID:     row.IdempotencyKey,
			Type:          row.Kind[len("event."):],
			ErrandID:      uuidString(row.ErrandID),
			ErrandVersion: data.ErrandVersion,
			Event:         data.Event,
			From:          data.From,
			To:            data.To,
			CourierID:     data.CourierID,
			Reward:        data.Reward,
			OccurredAt:    data.At,
		})
	}
	return out, nil
}

// ExportErrands returns up to limit errands with ID above afterID (empty for
// the first page), by ID.
func (r *Repository) ExportErrands(ctx context.Context, afterID string, limit int) ([]ExportedErrand, error) {
	if err := checkExportLimit(limit); err != nil {
		return nil, err
	}
	var after pgtype.UUID
	if afterID != "" {
		var err error
		if after, err = parseUUID("after", afterID); err != nil {
			return nil, err
		}
	}
	rows, err := r.queries.ExportErrandsPage(ctx, opsgen.ExportErrandsPageParams{AfterID: after, RowLimit: int32(limit)})
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]ExportedErrand, 0, len(rows))
	for _, row := range rows {
		out = append(out, exportedErrand(row.ID, row.Status, row.Version, row.CourierID, row.Reward, row.SupplierID,
			row.SupplierVersionID, row.DeliveryBuildingID, row.DeliveryBuildingName, row.CreatedAt, row.UpdatedAt))
	}
	return out, nil
}

// ExportErrandsByID returns the listed errands that exist, by ID. Unknown
// IDs are left out.
func (r *Repository) ExportErrandsByID(ctx context.Context, ids []string) ([]ExportedErrand, error) {
	if len(ids) == 0 || len(ids) > MaxExportPageSize {
		return nil, invalid("ids must hold between 1 and %d errand IDs", MaxExportPageSize)
	}
	parsed := make([]pgtype.UUID, 0, len(ids))
	for _, id := range ids {
		u, err := parseUUID("ids", id)
		if err != nil {
			return nil, err
		}
		parsed = append(parsed, u)
	}
	rows, err := r.queries.ExportErrandsByID(ctx, parsed)
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]ExportedErrand, 0, len(rows))
	for _, row := range rows {
		out = append(out, exportedErrand(row.ID, row.Status, row.Version, row.CourierID, row.Reward, row.SupplierID,
			row.SupplierVersionID, row.DeliveryBuildingID, row.DeliveryBuildingName, row.CreatedAt, row.UpdatedAt))
	}
	return out, nil
}

func exportedErrand(id pgtype.UUID, status string, version int32, courier pgtype.Text, reward int32, supplier,
	supplierVersion, building pgtype.UUID, buildingName string, created, updated pgtype.Timestamptz) ExportedErrand {
	return ExportedErrand{
		ID:                   uuidString(id),
		Status:               errand.Status(status),
		Version:              int(version),
		CourierID:            courier.String,
		Reward:               int(reward),
		SupplierID:           uuidString(supplier),
		SupplierVersionID:    uuidString(supplierVersion),
		DeliveryBuildingID:   uuidString(building),
		DeliveryBuildingName: buildingName,
		CreatedAt:            timeOrZero(created),
		UpdatedAt:            timeOrZero(updated),
	}
}

// checkExportLimit allows one row beyond MaxExportPageSize, which callers
// use to tell whether another page exists.
func checkExportLimit(limit int) error {
	if limit <= 0 || limit > MaxExportPageSize+1 {
		return invalid("limit must be between 1 and %d", MaxExportPageSize+1)
	}
	return nil
}
