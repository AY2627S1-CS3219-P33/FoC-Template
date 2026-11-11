// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-09
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

package review

import (
	"context"
	"fmt"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/opsrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

// OverviewReport is what the overview route needs.
type OverviewReport interface {
	Get(ctx context.Context) (Overview, error)
}

// OverviewCounter reads the live counts; *opsrepo.Repository implements it.
type OverviewCounter interface {
	Overview(ctx context.Context) (opsrepo.OverviewCounts, error)
}

// Overview is the live operational state at AsOf.
type Overview struct {
	AsOf              time.Time
	Ongoing           int
	WaitingForCourier int
	OpenReviews       int
	// OldestReviewAt is nil when there are no open reviews.
	OldestReviewAt *time.Time
	// OldestReviewAge is measured to AsOf; zero when OldestReviewAt is nil.
	OldestReviewAge time.Duration
}

// OverviewService implements OverviewReport at the Clock's now.
type OverviewService struct {
	counts OverviewCounter
	clock  errand.Clock
}

var _ OverviewReport = (*OverviewService)(nil)

func NewOverviewService(counts OverviewCounter, clock errand.Clock) *OverviewService {
	switch {
	case counts == nil:
		panic("review: overview counter is required")
	case clock == nil:
		panic("review: clock is required")
	}
	return &OverviewService{counts: counts, clock: clock}
}

func (s *OverviewService) Get(ctx context.Context) (Overview, error) {
	// Take the time first, so a review created while the counts were read
	// is never newer than asOf.
	asOf := s.clock.Now()
	counts, err := s.counts.Overview(ctx)
	if err != nil {
		return Overview{}, fmt.Errorf("read overview: %w", err)
	}
	overview := Overview{
		AsOf:              asOf,
		Ongoing:           counts.Ongoing,
		WaitingForCourier: counts.WaitingForCourier,
		OpenReviews:       counts.OpenReviews,
		OldestReviewAt:    counts.OldestReviewAt,
	}
	if counts.OldestReviewAt != nil {
		// A review created after asOf would give a negative age.
		overview.OldestReviewAge = max(asOf.Sub(*counts.OldestReviewAt), 0)
	}
	return overview, nil
}
