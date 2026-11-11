// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-10-22
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

package review

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/opsrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

// FlagReport is what the suspicious-activity route needs (F6.3).
type FlagReport interface {
	List(ctx context.Context) ([]Flag, error)
}

// ActivityCounter counts withdrawals, cancellations and completions per user;
// *opsrepo.Repository implements it.
type ActivityCounter interface {
	ListWithdrawalCounts(ctx context.Context, since time.Time, minCount, limit int) ([]opsrepo.ActivityCount, error)
	ListCancellationCounts(ctx context.Context, since time.Time, minCount, limit int) ([]opsrepo.ActivityCount, error)
	ListCompletionCounts(ctx context.Context, since time.Time, minCount, limit int) ([]opsrepo.ActivityCount, error)
}

// FlagKind is what a flag is about.
type FlagKind string

const (
	// FlagCourierWithdrawals: a courier reached the withdrawal-restriction
	// count within its window (F6.3.1, F3.6.4).
	FlagCourierWithdrawals FlagKind = "COURIER_WITHDRAWALS"
	// FlagRequesterCancellations: a requester exceeded the cancellation-flag
	// count within its window (F6.3.2, F3.5.3).
	FlagRequesterCancellations FlagKind = "REQUESTER_CANCELLATIONS"
	// FlagCourierCompletions: a courier reached the completion-flag count
	// within its window. Completions by an admin do not count.
	FlagCourierCompletions FlagKind = "COURIER_COMPLETIONS"
)

// MaxFlags caps the flag list (FlagList in openapi/common.yaml).
const MaxFlags = opsrepo.MaxActivityRows

// Flag is one user over a threshold.
type Flag struct {
	UserID string
	Kind   FlagKind
	// Count is the user's withdrawals, cancellations or completions within
	// the window.
	Count int
	// Threshold is the settings count the user reached or exceeded.
	Threshold int
	Window    time.Duration
	// Since is the start of the window evaluated.
	Since time.Time
	// Summary is the human-readable trigger (F6.3.4).
	Summary string
}

// FlagService implements FlagReport from errand_transitions and the current
// Settings, evaluated at the Clock's now.
type FlagService struct {
	counts   ActivityCounter
	settings errand.Settings
	clock    errand.Clock
}

var _ FlagReport = (*FlagService)(nil)

func NewFlagService(counts ActivityCounter, settings errand.Settings, clock errand.Clock) *FlagService {
	switch {
	case counts == nil:
		panic("review: activity counter is required")
	case settings == nil:
		panic("review: settings are required")
	case clock == nil:
		panic("review: clock is required")
	}
	return &FlagService{counts: counts, settings: settings, clock: clock}
}

// List returns every flag, most events first, then by user ID and kind,
// capped at MaxFlags. A courier is flagged at the withdrawal-restriction
// count (the third withdrawal restricts, F3.6.4) and at the completion-flag
// count; a requester only above the cancellation-flag count ("more than",
// F3.5.3).
func (s *FlagService) List(ctx context.Context) ([]Flag, error) {
	settings, err := s.settings.Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("read settings: %w", err)
	}
	now := s.clock.Now()

	withdrawals := settings.WithdrawalRestriction
	withdrawalsSince := now.Add(-withdrawals.Window)
	couriers, err := s.counts.ListWithdrawalCounts(ctx, withdrawalsSince, withdrawals.Count, MaxFlags)
	if err != nil {
		return nil, fmt.Errorf("count withdrawals: %w", err)
	}
	cancellations := settings.CancellationFlag
	cancellationsSince := now.Add(-cancellations.Window)
	requesters, err := s.counts.ListCancellationCounts(ctx, cancellationsSince, cancellations.Count+1, MaxFlags)
	if err != nil {
		return nil, fmt.Errorf("count cancellations: %w", err)
	}

	completions := settings.CompletionFlag
	completionsSince := now.Add(-completions.Window)
	finishers, err := s.counts.ListCompletionCounts(ctx, completionsSince, completions.Count, MaxFlags)
	if err != nil {
		return nil, fmt.Errorf("count completions: %w", err)
	}

	flags := make([]Flag, 0, len(couriers)+len(requesters)+len(finishers))
	for _, c := range couriers {
		flags = append(flags, Flag{
			UserID: c.UserID, Kind: FlagCourierWithdrawals, Count: c.Count,
			Threshold: withdrawals.Count, Window: withdrawals.Window, Since: withdrawalsSince,
			Summary: fmt.Sprintf("High withdrawal rate, %d in %s", c.Count, formatWindow(withdrawals.Window)),
		})
	}
	for _, r := range requesters {
		flags = append(flags, Flag{
			UserID: r.UserID, Kind: FlagRequesterCancellations, Count: r.Count,
			Threshold: cancellations.Count, Window: cancellations.Window, Since: cancellationsSince,
			Summary: fmt.Sprintf("High cancellation rate, %d in %s", r.Count, formatWindow(cancellations.Window)),
		})
	}
	for _, f := range finishers {
		flags = append(flags, Flag{
			UserID: f.UserID, Kind: FlagCourierCompletions, Count: f.Count,
			Threshold: completions.Count, Window: completions.Window, Since: completionsSince,
			Summary: fmt.Sprintf("High completion rate, %d in %s", f.Count, formatWindow(completions.Window)),
		})
	}
	slices.SortFunc(flags, func(a, b Flag) int {
		return cmp.Or(cmp.Compare(b.Count, a.Count), cmp.Compare(a.UserID, b.UserID), cmp.Compare(a.Kind, b.Kind))
	})
	if len(flags) > MaxFlags {
		flags = flags[:MaxFlags]
	}
	return flags, nil
}

// formatWindow writes a window as "15 min", "1 h" or "90 s".
func formatWindow(d time.Duration) string {
	switch {
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%d h", d/time.Hour)
	case d >= time.Minute && d%time.Minute == 0:
		return fmt.Sprintf("%d min", d/time.Minute)
	default:
		return fmt.Sprintf("%d s", d/time.Second)
	}
}
