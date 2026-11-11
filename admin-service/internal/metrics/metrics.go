// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-09
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

// Package metrics computes the dashboard's metrics 5 to 11 from the read
// model at query time. The definitions (docs in the plan, and the comments in
// queries/metrics.sql) are:
//
//   - Errands per day: by the errand.opened time, not the creation time.
//   - Time to accept: first accept minus opened, median and p90, for errands
//     whose first accept falls in the period; never-accepted errands excluded.
//   - Outcomes: over errands that finished in the period, all five terminal
//     statuses, so the shares add to 100%.
//   - Top suppliers and buildings: ranked by errands finished in the period,
//     with the completed count beside it.
//   - Active couriers: distinct couriers with at least one accept in the period.
//   - Reward volume: the reward of COMPLETED errands, by the day they completed.
//
// A period is whole days in the dashboard's time zone, both ends included.
package metrics

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/database/admingen"
	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/facts"
)

const (
	// DefaultDays is the period when none is given: today and the 29 days
	// before it.
	DefaultDays = 30
	// MaxDays bounds a period.
	MaxDays = 366
	// MaxRankLimit bounds a ranking.
	MaxRankLimit = 50
	// DefaultRankLimit is a ranking's length when none is given.
	DefaultRankLimit = 10

	dateLayout = "2006-01-02"
)

// ErrInvalidPeriod means the period is malformed or out of range.
var ErrInvalidPeriod = errors.New("metrics: invalid period")

// PeriodError says what is wrong with a period, in words safe to return to
// the caller. It matches ErrInvalidPeriod.
type PeriodError struct{ Message string }

func (e *PeriodError) Error() string { return "metrics: invalid period: " + e.Message }

func (e *PeriodError) Is(target error) bool { return target == ErrInvalidPeriod }

// Period is a run of whole days, both ends included, in Location.
type Period struct {
	Location *time.Location
	// From and To are the first and last day, "YYYY-MM-DD".
	From, To string
	// Start is the first day's midnight and End the midnight after the last
	// day; queries read [Start, End).
	Start, End time.Time
}

// Days lists every day of the period, "YYYY-MM-DD", oldest first.
func (p Period) Days() []string {
	var days []string
	for day := p.Start; day.Before(p.End); day = day.AddDate(0, 0, 1) {
		days = append(days, day.Format(dateLayout))
	}
	return days
}

// NewPeriod parses from and to, "YYYY-MM-DD" in loc, both included. An empty
// to is today; an empty from is DefaultDays-1 days before to. now is the
// current time.
func NewPeriod(from, to string, now time.Time, loc *time.Location) (Period, error) {
	if loc == nil {
		return Period{}, errors.New("metrics: a time zone is required")
	}
	today := time.Date(now.In(loc).Year(), now.In(loc).Month(), now.In(loc).Day(), 0, 0, 0, 0, loc)
	last, first := today, today.AddDate(0, 0, -(DefaultDays-1))
	var err error
	if to != "" {
		if last, err = time.ParseInLocation(dateLayout, to, loc); err != nil {
			return Period{}, &PeriodError{"to must be a date, YYYY-MM-DD"}
		}
		if from == "" {
			first = last.AddDate(0, 0, -(DefaultDays - 1))
		}
	}
	if from != "" {
		if first, err = time.ParseInLocation(dateLayout, from, loc); err != nil {
			return Period{}, &PeriodError{"from must be a date, YYYY-MM-DD"}
		}
	}
	if first.After(last) {
		return Period{}, &PeriodError{"from must not be after to"}
	}
	end := last.AddDate(0, 0, 1)
	if first.AddDate(0, 0, MaxDays).Before(end) {
		return Period{}, &PeriodError{fmt.Sprintf("a period is at most %d days", MaxDays)}
	}
	return Period{
		Location: loc, From: first.Format(dateLayout), To: last.Format(dateLayout), Start: first, End: end,
	}, nil
}

// Service reads the metrics. It is safe for concurrent use when its
// database handle is.
type Service struct {
	queries *admingen.Queries
	loc     *time.Location
}

// NewService reads from db and groups days in loc.
func NewService(db admingen.DBTX, loc *time.Location) *Service {
	if db == nil || loc == nil {
		panic("metrics: a database and a time zone are required")
	}
	return &Service{queries: admingen.New(db), loc: loc}
}

// Location is the time zone days are counted in.
func (s *Service) Location() *time.Location { return s.loc }

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

// DayCount is a day's errands.
type DayCount struct {
	Date    string
	Created int
}

// ErrandsPerDay is metric 5. Every day of the period is listed, with zero
// for a day nothing opened.
func (s *Service) ErrandsPerDay(ctx context.Context, p Period) ([]DayCount, error) {
	rows, err := s.queries.CountOpenedPerDay(ctx, admingen.CountOpenedPerDayParams{
		Tz: s.loc.String(), FromAt: ts(p.Start), ToAt: ts(p.End),
	})
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, row := range rows {
		counts[row.Day.Time.Format(dateLayout)] = int(row.Created)
	}
	days := p.Days()
	out := make([]DayCount, 0, len(days))
	for _, day := range days {
		out = append(out, DayCount{Date: day, Created: counts[day]})
	}
	return out, nil
}

// TimeToAccept is metric 6. Median and P90 are nil when nothing was
// accepted in the period.
type TimeToAccept struct {
	Accepted      int
	MedianSeconds *float64
	P90Seconds    *float64
}

func (s *Service) TimeToAccept(ctx context.Context, p Period) (TimeToAccept, error) {
	row, err := s.queries.SummarizeTimeToAccept(ctx, admingen.SummarizeTimeToAcceptParams{FromAt: ts(p.Start), ToAt: ts(p.End)})
	if err != nil {
		return TimeToAccept{}, err
	}
	result := TimeToAccept{Accepted: int(row.Accepted)}
	if row.Accepted > 0 {
		median, p90 := row.MedianSeconds, row.P90Seconds
		result.MedianSeconds, result.P90Seconds = &median, &p90
	}
	return result, nil
}

// Outcome is one terminal status's share of the errands that finished.
type Outcome struct {
	Status facts.Status
	Count  int
	// Rate is Count over the finished total; 0 when nothing finished.
	Rate float64
}

// Outcomes is metric 7.
type Outcomes struct {
	// Finished is how many errands reached a terminal state in the period.
	Finished int
	// Items has all five terminal statuses, in facts.TerminalStatuses order,
	// so cancelled and refunded show as their own segments and the rates add
	// to 1.
	Items []Outcome
}

func (s *Service) Outcomes(ctx context.Context, p Period) (Outcomes, error) {
	rows, err := s.queries.CountOutcomes(ctx, admingen.CountOutcomesParams{FromAt: ts(p.Start), ToAt: ts(p.End)})
	if err != nil {
		return Outcomes{}, err
	}
	counts := map[facts.Status]int{}
	finished := 0
	for _, row := range rows {
		counts[facts.Status(row.TerminalStatus.String)] += int(row.Finished)
		finished += int(row.Finished)
	}
	result := Outcomes{Finished: finished}
	for _, status := range facts.TerminalStatuses() {
		item := Outcome{Status: status, Count: counts[status]}
		if finished > 0 {
			item.Rate = float64(item.Count) / float64(finished)
		}
		result.Items = append(result.Items, item)
	}
	return result, nil
}

// Ranked is one supplier or building in a ranking. Name is nil when no
// finished errand of it carried one.
type Ranked struct {
	ID        string
	Name      *string
	Finished  int
	Completed int
}

// Ranking is metrics 8 and 9.
type Ranking struct {
	Items []Ranked
	// Unattributed counts errands finished in the period that are not in the
	// ranking because enrichment has not stored their supplier or building.
	Unattributed int
}

// TopSuppliers is metric 8: the pickup suppliers with the most errands
// finished in the period.
func (s *Service) TopSuppliers(ctx context.Context, p Period, limit int) (Ranking, error) {
	if err := checkLimit(limit); err != nil {
		return Ranking{}, err
	}
	rows, err := s.queries.RankSuppliers(ctx, admingen.RankSuppliersParams{FromAt: ts(p.Start), ToAt: ts(p.End), RowLimit: int32(limit)})
	if err != nil {
		return Ranking{}, err
	}
	unattributed, err := s.queries.CountUnattributed(ctx, admingen.CountUnattributedParams{FromAt: ts(p.Start), ToAt: ts(p.End)})
	if err != nil {
		return Ranking{}, err
	}
	ranking := Ranking{Items: make([]Ranked, 0, len(rows)), Unattributed: int(unattributed.WithoutSupplier)}
	for _, row := range rows {
		ranking.Items = append(ranking.Items, Ranked{
			ID: uuidString(row.SupplierID), Name: nonEmpty(row.Name), Finished: int(row.Finished), Completed: int(row.Completed),
		})
	}
	return ranking, nil
}

// TopBuildings is metric 9: the delivery buildings with the most errands
// finished in the period.
func (s *Service) TopBuildings(ctx context.Context, p Period, limit int) (Ranking, error) {
	if err := checkLimit(limit); err != nil {
		return Ranking{}, err
	}
	rows, err := s.queries.RankBuildings(ctx, admingen.RankBuildingsParams{FromAt: ts(p.Start), ToAt: ts(p.End), RowLimit: int32(limit)})
	if err != nil {
		return Ranking{}, err
	}
	unattributed, err := s.queries.CountUnattributed(ctx, admingen.CountUnattributedParams{FromAt: ts(p.Start), ToAt: ts(p.End)})
	if err != nil {
		return Ranking{}, err
	}
	ranking := Ranking{Items: make([]Ranked, 0, len(rows)), Unattributed: int(unattributed.WithoutBuilding)}
	for _, row := range rows {
		ranking.Items = append(ranking.Items, Ranked{
			ID: uuidString(row.BuildingID), Name: nonEmpty(row.Name), Finished: int(row.Finished), Completed: int(row.Completed),
		})
	}
	return ranking, nil
}

// ActiveCouriers is metric 10: distinct couriers with at least one accept in
// the period.
func (s *Service) ActiveCouriers(ctx context.Context, p Period) (int, error) {
	n, err := s.queries.CountActiveCouriers(ctx, admingen.CountActiveCouriersParams{FromAt: ts(p.Start), ToAt: ts(p.End)})
	return int(n), err
}

// DayReward is a day's completed reward.
type DayReward struct {
	Date      string
	Reward    int64
	Completed int
}

// RewardVolume is metric 11. Every day of the period is listed.
func (s *Service) RewardVolume(ctx context.Context, p Period) ([]DayReward, error) {
	rows, err := s.queries.SumCompletedRewardPerDay(ctx, admingen.SumCompletedRewardPerDayParams{
		Tz: s.loc.String(), FromAt: ts(p.Start), ToAt: ts(p.End),
	})
	if err != nil {
		return nil, err
	}
	byDay := map[string]DayReward{}
	for _, row := range rows {
		day := row.Day.Time.Format(dateLayout)
		byDay[day] = DayReward{Date: day, Reward: row.Reward, Completed: int(row.Completed)}
	}
	days := p.Days()
	out := make([]DayReward, 0, len(days))
	for _, day := range days {
		item, ok := byDay[day]
		if !ok {
			item = DayReward{Date: day}
		}
		out = append(out, item)
	}
	return out, nil
}

func checkLimit(limit int) error {
	if limit < 1 || limit > MaxRankLimit {
		return fmt.Errorf("metrics: limit must be between 1 and %d", MaxRankLimit)
	}
	return nil
}

func nonEmpty(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func uuidString(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	value, _ := id.Value()
	s, _ := value.(string)
	return s
}
