package errandrepo

import (
	"encoding/json"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/errandgen"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

// errandFromRow converts a stored row. pending_edit holds the edit's complete
// errand.Details as JSON; its key and time are separate columns.
func errandFromRow(row errandgen.Errand) (errand.Errand, error) {
	e := errand.Errand{
		ID:          uuidString(row.ID),
		Status:      errand.Status(row.Status),
		Version:     int(row.Version),
		RequesterID: row.RequesterID,
		CourierID:   row.CourierID.String,
		Details: errand.Details{
			SupplierID:               uuidString(row.SupplierID),
			SupplierVersionID:        uuidString(row.SupplierVersionID),
			DeliveryBuildingID:       uuidString(row.DeliveryBuildingID),
			DeliveryBuildingSnapshot: json.RawMessage(row.DeliveryBuildingSnapshot),
			DeliveryDetail:           row.DeliveryDetail,
			Items:                    json.RawMessage(row.Items),
			Notes:                    row.Notes,
			ContactName:              row.ContactName,
			ContactMobile:            row.ContactMobile,
			Reward:                   int(row.Reward),
			AcceptanceDeadline:       row.AcceptanceDeadline.Time,
		},
		PickupDeadline:       timePtr(row.PickupDeadline),
		DeliveryDeadline:     timePtr(row.DeliveryDeadline),
		DeliveredAt:          timePtr(row.DeliveredAt),
		VerificationDeadline: timePtr(row.VerificationDeadline),
		ReviewReason:         errand.ReviewReason(row.ReviewReason.String),
		CreateIdempotencyKey: row.CreateIdempotencyKey,
		CreatedAt:            row.CreatedAt.Time,
		UpdatedAt:            row.UpdatedAt.Time,
	}
	if row.PendingEdit != nil {
		var details errand.Details
		if err := json.Unmarshal(row.PendingEdit, &details); err != nil {
			return errand.Errand{}, fmt.Errorf("errandrepo: errand %s has an unreadable pending edit: %w", e.ID, err)
		}
		e.PendingEdit = &errand.PendingEdit{
			Details: details,
			Key:     row.PendingEditKey.String,
			At:      row.PendingEditAt.Time,
		}
	}
	return e, nil
}

func errandsFromRows(rows []errandgen.Errand) ([]errand.Errand, error) {
	out := make([]errand.Errand, 0, len(rows))
	for _, row := range rows {
		e, err := errandFromRow(row)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// insertParams converts e, the errand as a transition leaves it, into a
// complete row. Malformed IDs and out-of-range rewards are INVALID_ARGUMENT.
func insertParams(e errand.Errand) (errandgen.InsertErrandParams, error) {
	var p errandgen.InsertErrandParams
	ids := []struct {
		name  string
		value string
		dest  *pgtype.UUID
	}{
		{"errandId", e.ID, &p.ID},
		{"supplierId", e.Details.SupplierID, &p.SupplierID},
		{"supplierVersionId", e.Details.SupplierVersionID, &p.SupplierVersionID},
		{"deliveryBuildingId", e.Details.DeliveryBuildingID, &p.DeliveryBuildingID},
	}
	for _, id := range ids {
		parsed, err := parseUUID(id.name, id.value)
		if err != nil {
			return p, err
		}
		*id.dest = parsed
	}
	if e.Details.Reward > math.MaxInt32 || e.Version > math.MaxInt32 {
		return p, appErr(apperror.InvalidArgument, "reward or version is out of range")
	}
	var pendingEdit []byte
	var pendingKey pgtype.Text
	var pendingAt pgtype.Timestamptz
	if e.PendingEdit != nil {
		var err error
		if pendingEdit, err = json.Marshal(e.PendingEdit.Details); err != nil {
			return p, fmt.Errorf("errandrepo: encode pending edit: %w", err)
		}
		pendingKey = pgtype.Text{String: e.PendingEdit.Key, Valid: true}
		pendingAt = timestamp(e.PendingEdit.At)
	}

	p.Status = string(e.Status)
	p.Version = int32(e.Version)
	p.RequesterID = e.RequesterID
	p.CourierID = text(e.CourierID)
	p.DeliveryBuildingSnapshot = e.Details.DeliveryBuildingSnapshot
	p.DeliveryDetail = e.Details.DeliveryDetail
	p.Items = e.Details.Items
	p.Notes = e.Details.Notes
	p.ContactName = e.Details.ContactName
	p.ContactMobile = e.Details.ContactMobile
	p.Reward = int32(e.Details.Reward)
	p.AcceptanceDeadline = timestamp(e.Details.AcceptanceDeadline)
	p.PickupDeadline = nullTimestamp(e.PickupDeadline)
	p.DeliveryDeadline = nullTimestamp(e.DeliveryDeadline)
	p.DeliveredAt = nullTimestamp(e.DeliveredAt)
	p.VerificationDeadline = nullTimestamp(e.VerificationDeadline)
	p.ReviewReason = text(string(e.ReviewReason))
	p.CreateIdempotencyKey = e.CreateIdempotencyKey
	p.PendingEdit = pendingEdit
	p.PendingEditKey = pendingKey
	p.PendingEditAt = pendingAt
	p.CreatedAt = timestamp(e.CreatedAt)
	p.UpdatedAt = timestamp(e.UpdatedAt)
	return p, nil
}

// updateParams writes after, the errand as t leaves it, under t's
// compare-and-swap condition.
func updateParams(after errand.Errand, t errand.Transition) (errandgen.UpdateErrandParams, error) {
	row, err := insertParams(after)
	if err != nil {
		return errandgen.UpdateErrandParams{}, err
	}
	return errandgen.UpdateErrandParams{
		Status:                   row.Status,
		Version:                  row.Version,
		CourierID:                row.CourierID,
		SupplierID:               row.SupplierID,
		SupplierVersionID:        row.SupplierVersionID,
		DeliveryBuildingID:       row.DeliveryBuildingID,
		DeliveryBuildingSnapshot: row.DeliveryBuildingSnapshot,
		DeliveryDetail:           row.DeliveryDetail,
		Items:                    row.Items,
		Notes:                    row.Notes,
		ContactName:              row.ContactName,
		ContactMobile:            row.ContactMobile,
		Reward:                   row.Reward,
		AcceptanceDeadline:       row.AcceptanceDeadline,
		PickupDeadline:           row.PickupDeadline,
		DeliveryDeadline:         row.DeliveryDeadline,
		DeliveredAt:              row.DeliveredAt,
		VerificationDeadline:     row.VerificationDeadline,
		ReviewReason:             row.ReviewReason,
		PendingEdit:              row.PendingEdit,
		PendingEditKey:           row.PendingEditKey,
		PendingEditAt:            row.PendingEditAt,
		UpdatedAt:                row.UpdatedAt,
		ID:                       row.ID,
		ExpectedStatus:           string(t.From),
		ExpectedVersion:          int32(t.ExpectedVersion),
		ExpectedPendingEditKey:   text(t.ExpectedPendingEditKey),
	}, nil
}
