package supplierclient

import (
	"fmt"
	"time"
)

// singapore is the zone supplier opening hours are written in. Singapore has
// had a fixed UTC+8 offset with no daylight saving since 1982, so the fixed
// zone is an exact fallback when the host has no tz database.
var singapore = func() *time.Location {
	if loc, err := time.LoadLocation("Asia/Singapore"); err == nil {
		return loc
	}
	return time.FixedZone("Asia/Singapore", 8*60*60)
}()

// parseClock parses "HH:MM" into minutes after midnight.
func parseClock(s string) (int, error) {
	t, err := time.Parse("15:04", s)
	if err != nil || len(s) != len("15:04") {
		return 0, fmt.Errorf("invalid time of day %q", s)
	}
	return t.Hour()*60 + t.Minute(), nil
}

// openAt reports whether hours opening–closing cover now, read in
// Asia/Singapore. The interval is [opening, closing) at minute resolution. A
// closing time at or before the opening time crosses midnight, so equal
// times cover the whole day.
func openAt(opening, closing string, now time.Time) (bool, error) {
	from, err := parseClock(opening)
	if err != nil {
		return false, err
	}
	to, err := parseClock(closing)
	if err != nil {
		return false, err
	}
	local := now.In(singapore)
	m := local.Hour()*60 + local.Minute()
	if from < to {
		return from <= m && m < to, nil
	}
	return m >= from || m < to, nil
}
