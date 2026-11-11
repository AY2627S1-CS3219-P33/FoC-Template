// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-10-18
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

package errand

import "time"

// Clock is the only source of the current time for errand logic, so tests
// can control deadlines. Domain functions take now as an argument instead.
type Clock interface {
	Now() time.Time
}

// SystemClock reads the wall clock. Tests use testfakes.FakeClock instead.
type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now() }

var _ Clock = SystemClock{}
