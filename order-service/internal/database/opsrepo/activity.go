package opsrepo

import (
	"context"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/opsgen"
)

// MaxActivityRows bounds the suspicious-activity lists (FlagList in
// openapi/common.yaml is capped at 500).
const MaxActivityRows = 500

// ActivityCount is how many withdrawals or cancellations one user has.
type ActivityCount struct {
	UserID string
	Count  int
}

// ListWithdrawalCounts returns the couriers with at least minCount
// withdrawals, voluntary or forced, at or after since (F6.3.1, F3.6.4),
// most first, then by user ID.
func (r *Repository) ListWithdrawalCounts(ctx context.Context, since time.Time, minCount, limit int) ([]ActivityCount, error) {
	if err := checkActivityArgs(since, minCount, limit); err != nil {
		return nil, err
	}
	rows, err := r.queries.ListWithdrawalCounts(ctx, opsgen.ListWithdrawalCountsParams{
		Since: timestamp(since), MinCount: int64(minCount), RowLimit: int32(limit),
	})
	if err != nil {
		return nil, mapError(err)
	}
	counts := make([]ActivityCount, 0, len(rows))
	for _, row := range rows {
		counts = append(counts, ActivityCount{UserID: row.UserID, Count: int(row.EventCount)})
	}
	return counts, nil
}

// ListCancellationCounts returns the requesters with at least minCount
// cancellations at or after since (F6.3.2, F3.5.3), most first, then by
// user ID.
func (r *Repository) ListCancellationCounts(ctx context.Context, since time.Time, minCount, limit int) ([]ActivityCount, error) {
	if err := checkActivityArgs(since, minCount, limit); err != nil {
		return nil, err
	}
	rows, err := r.queries.ListCancellationCounts(ctx, opsgen.ListCancellationCountsParams{
		Since: timestamp(since), MinCount: int64(minCount), RowLimit: int32(limit),
	})
	if err != nil {
		return nil, mapError(err)
	}
	counts := make([]ActivityCount, 0, len(rows))
	for _, row := range rows {
		counts = append(counts, ActivityCount{UserID: row.UserID, Count: int(row.EventCount)})
	}
	return counts, nil
}

func checkActivityArgs(since time.Time, minCount, limit int) error {
	switch {
	case since.IsZero():
		return invalid("since is required")
	case minCount < 1:
		return invalid("minCount must be positive")
	case limit < 1 || limit > MaxActivityRows:
		return invalid("limit must be between 1 and %d", MaxActivityRows)
	}
	return nil
}
