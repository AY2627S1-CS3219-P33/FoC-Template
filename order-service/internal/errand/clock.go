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
