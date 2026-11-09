package metrics

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var sgt = mustLoad("Asia/Singapore")

func mustLoad(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

func TestNewPeriodDefaultsToTheLastThirtyDaysEndingToday(t *testing.T) {
	// 2026-03-02 00:30 in Singapore is still 2026-03-01 in UTC.
	now := time.Date(2026, 3, 1, 16, 30, 0, 0, time.UTC)

	p, err := NewPeriod("", "", now, sgt)

	require.NoError(t, err)
	require.Equal(t, "2026-03-02", p.To, "today is the Singapore date")
	require.Equal(t, "2026-02-01", p.From, "thirty days including today")
	require.Len(t, p.Days(), 30)
	require.Equal(t, time.Date(2026, 2, 1, 0, 0, 0, 0, sgt), p.Start)
	require.Equal(t, time.Date(2026, 3, 3, 0, 0, 0, 0, sgt), p.End, "the end is the midnight after the last day")
	require.Equal(t, time.Date(2026, 1, 31, 16, 0, 0, 0, time.UTC), p.Start.UTC(), "Singapore is UTC+8")
}

func TestNewPeriodExplicitDatesAreInclusive(t *testing.T) {
	p, err := NewPeriod("2026-03-01", "2026-03-03", time.Now(), sgt)

	require.NoError(t, err)
	require.Equal(t, []string{"2026-03-01", "2026-03-02", "2026-03-03"}, p.Days())
	require.Equal(t, 3*24*time.Hour, p.End.Sub(p.Start))
}

func TestNewPeriodSingleDay(t *testing.T) {
	p, err := NewPeriod("2026-03-01", "2026-03-01", time.Now(), sgt)

	require.NoError(t, err)
	require.Equal(t, []string{"2026-03-01"}, p.Days())
}

func TestNewPeriodOnlyToOrOnlyFrom(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, sgt)

	onlyTo, err := NewPeriod("", "2026-02-15", now, sgt)
	require.NoError(t, err)
	require.Equal(t, "2026-01-17", onlyTo.From, "thirty days ending at to")

	onlyFrom, err := NewPeriod("2026-03-01", "", now, sgt)
	require.NoError(t, err)
	require.Equal(t, "2026-03-10", onlyFrom.To, "up to today")
	require.Len(t, onlyFrom.Days(), 10)
}

func TestNewPeriodLimits(t *testing.T) {
	now := time.Date(2026, 12, 31, 12, 0, 0, 0, sgt)

	_, err := NewPeriod("2026-01-01", "2026-12-31", now, sgt)
	require.NoError(t, err, "365 days")
	_, err = NewPeriod("2026-01-01", "2027-01-01", now, sgt)
	require.NoError(t, err, "366 days is the most")
	_, err = NewPeriod("2025-12-31", "2027-01-01", now, sgt)
	require.ErrorIs(t, err, ErrInvalidPeriod, "367 days")
}

func TestNewPeriodRejectsBadInput(t *testing.T) {
	now := time.Now()
	for name, tc := range map[string]struct{ from, to, message string }{
		"from not a date": {"yesterday", "", "from must be a date, YYYY-MM-DD"},
		"to not a date":   {"", "2026-13-01", "to must be a date, YYYY-MM-DD"},
		"with a time":     {"2026-03-01T00:00:00Z", "", "from must be a date, YYYY-MM-DD"},
		"from after to":   {"2026-03-02", "2026-03-01", "from must not be after to"},
		"too long":        {"2020-01-01", "2026-03-01", "a period is at most 366 days"},
		"impossible date": {"2026-02-30", "", "from must be a date, YYYY-MM-DD"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewPeriod(tc.from, tc.to, now, sgt)

			require.ErrorIs(t, err, ErrInvalidPeriod)
			var periodErr *PeriodError
			require.ErrorAs(t, err, &periodErr)
			require.Equal(t, tc.message, periodErr.Message)
		})
	}
	_, err := NewPeriod("", "", now, nil)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrInvalidPeriod)
}
