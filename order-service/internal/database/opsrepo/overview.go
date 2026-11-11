// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-09
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

package opsrepo

import (
	"context"
	"time"
)

// OverviewCounts is the live state behind GET /admin/overview.
type OverviewCounts struct {
	// Ongoing counts ASSIGNED, PICKED_UP and DELIVERED errands.
	Ongoing int
	// WaitingForCourier counts OPEN errands.
	WaitingForCourier int
	// OpenReviews counts open reviews whose errand is REVIEW_REQUIRED.
	OpenReviews int
	// OldestReviewAt is the creation time of the oldest of those reviews;
	// nil when there are none.
	OldestReviewAt *time.Time
}

// Overview reads the admin overview counts. The two reads are separate
// statements, so the numbers are each current but not one snapshot.
func (r *Repository) Overview(ctx context.Context) (OverviewCounts, error) {
	errands, err := r.queries.CountOverviewErrands(ctx)
	if err != nil {
		return OverviewCounts{}, mapError(err)
	}
	reviews, err := r.queries.SummarizeOpenReviews(ctx)
	if err != nil {
		return OverviewCounts{}, mapError(err)
	}
	return OverviewCounts{
		Ongoing:           int(errands.Ongoing),
		WaitingForCourier: int(errands.WaitingForCourier),
		OpenReviews:       int(reviews.OpenCount),
		OldestReviewAt:    timePtr(reviews.OldestCreatedAt),
	}, nil
}
