package opsrepo

import (
	"context"
	"encoding/json"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/opsgen"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

// InsertReminder writes a reminder event row to the outbox. It is the only
// outbox write B makes (docs/contracts.md, "Who touches which rows"). A row
// with the same idempotency key already present is left as it is and
// inserted is false (NFR7.3). An unknown errand gives ErrNotFound.
func (r *Repository) InsertReminder(ctx context.Context, row errand.OutboxRow) (inserted bool, err error) {
	if !isReminderKind(row.Kind) {
		return false, invalid("%q is not a reminder kind", row.Kind)
	}
	if row.IdempotencyKey == "" {
		return false, invalid("idempotency key is required")
	}
	errandID, err := parseUUID("errandId", row.ErrandID)
	if err != nil {
		return false, err
	}
	payload, err := json.Marshal(row.Payload)
	if err != nil {
		return false, invalid("payload cannot be encoded: %v", err)
	}
	affected, err := r.queries.InsertReminder(ctx, opsgen.InsertReminderParams{
		Kind:           row.Kind,
		IdempotencyKey: row.IdempotencyKey,
		ErrandID:       errandID,
		Payload:        payload,
	})
	if err != nil {
		return false, mapError(err)
	}
	return affected == 1, nil
}

func isReminderKind(kind string) bool {
	return kind == errand.ErrandVerificationReminder.Kind() ||
		kind == errand.ErrandPickupDeadlineApproaching.Kind()
}
