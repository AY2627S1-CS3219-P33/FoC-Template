package resilience

import (
	"errors"
	"math"
	"math/rand/v2"
	"time"
)

// Default backoff bounds.
const (
	DefaultBackoffBase = 2 * time.Second
	DefaultBackoffCap  = 5 * time.Minute
)

// BackoffPolicy is exponential backoff with full jitter: the delay before
// attempt n (1-based) is drawn uniformly from [0, min(Cap, Base*2^(n-1))].
// It is safe for concurrent use if Rand is.
type BackoffPolicy struct {
	Base time.Duration
	Cap  time.Duration
	// Rand returns a uniform value in [0, n) for n > 0. Tests inject a
	// deterministic source; nil means math/rand/v2.Int64N.
	Rand func(n int64) int64
}

// DefaultBackoffPolicy returns base 2s, cap 5m and the global random source.
func DefaultBackoffPolicy() BackoffPolicy {
	return BackoffPolicy{Base: DefaultBackoffBase, Cap: DefaultBackoffCap}
}

// Validate reports a policy whose bounds make no sense.
func (p BackoffPolicy) Validate() error {
	switch {
	case p.Base <= 0:
		return errors.New("resilience: backoff base must be positive")
	case p.Cap < p.Base:
		return errors.New("resilience: backoff cap must be at least the base")
	}
	return nil
}

// Ceiling returns the upper bound of the jittered delay for attempt, without
// jitter. Attempts below 1 are treated as 1.
func (p BackoffPolicy) Ceiling(attempt int) time.Duration {
	d := p.Base
	for i := 1; i < attempt && d < p.Cap; i++ {
		if d > p.Cap/2 {
			d = p.Cap
			break
		}
		d *= 2
	}
	return min(d, p.Cap)
}

// Backoff returns the delay before attempt (1-based): uniform in
// [0, Ceiling(attempt)].
func (p BackoffPolicy) Backoff(attempt int) time.Duration {
	n := int64(p.Ceiling(attempt))
	if n <= 0 {
		return 0
	}
	if n < math.MaxInt64 {
		n++ // make the ceiling itself reachable
	}
	rnd := p.Rand
	if rnd == nil {
		rnd = rand.Int64N
	}
	return time.Duration(rnd(n))
}

// Backoff returns the delay before attempt under DefaultBackoffPolicy.
func Backoff(attempt int) time.Duration {
	return DefaultBackoffPolicy().Backoff(attempt)
}
