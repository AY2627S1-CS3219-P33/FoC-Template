package opsrepo

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/opsgen"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

// OpenReview is an unresolved errand_reviews row with the errand fields an
// admin needs to resolve it. Review rows are written by errand.Store with
// the transition; this package only reads them.
type OpenReview struct {
	ID       int64
	ErrandID string
	Reason   errand.ReviewReason
	// ReporterID is empty when the system raised the review.
	ReporterID string
	Note       string
	CreatedAt  time.Time
	Errand     ReviewedErrand
}

type ReviewedErrand struct {
	Status           errand.Status
	Version          int
	RequesterID      string
	CourierID        string
	SupplierID       string
	Reward           int
	DeliveryDeadline *time.Time
	DeliveredAt      *time.Time
	UpdatedAt        time.Time
}

// ListOpenReviews returns up to limit open reviews, oldest first, after the
// review with ID afterID (0 for the first page).
func (r *Repository) ListOpenReviews(ctx context.Context, afterID int64, limit int) ([]OpenReview, error) {
	if err := checkLimit(limit); err != nil {
		return nil, err
	}
	rows, err := r.queries.ListOpenReviews(ctx, opsgen.ListOpenReviewsParams{
		AfterID:  pgtype.Int8{Int64: afterID, Valid: afterID > 0},
		RowLimit: int32(limit),
	})
	if err != nil {
		return nil, mapError(err)
	}
	reviews := make([]OpenReview, 0, len(rows))
	for _, row := range rows {
		reviews = append(reviews, OpenReview{
			ID:         row.ID,
			ErrandID:   uuidString(row.ErrandID),
			Reason:     errand.ReviewReason(row.Reason),
			ReporterID: row.ReporterID.String,
			Note:       row.Note.String,
			CreatedAt:  timeOrZero(row.CreatedAt),
			Errand: ReviewedErrand{
				Status:           errand.Status(row.Status),
				Version:          int(row.Version),
				RequesterID:      row.RequesterID,
				CourierID:        row.CourierID.String,
				SupplierID:       uuidString(row.SupplierID),
				Reward:           int(row.Reward),
				DeliveryDeadline: timePtr(row.DeliveryDeadline),
				DeliveredAt:      timePtr(row.DeliveredAt),
				UpdatedAt:        timeOrZero(row.ErrandUpdatedAt),
			},
		})
	}
	return reviews, nil
}
