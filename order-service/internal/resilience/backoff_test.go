package resilience_test

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/resilience"
)

func TestBackoffPolicy_Ceiling(t *testing.T) {
	p := resilience.DefaultBackoffPolicy()
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{-1, 2 * time.Second},
		{0, 2 * time.Second},
		{1, 2 * time.Second},
		{2, 4 * time.Second},
		{3, 8 * time.Second},
		{8, 256 * time.Second},
		{9, 5 * time.Minute},
		{100, 5 * time.Minute},
		{math.MaxInt, 5 * time.Minute},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, p.Ceiling(tc.attempt), "attempt %d", tc.attempt)
	}
}

func TestBackoffPolicy_Bounds(t *testing.T) {
	cases := []struct {
		name    string
		policy  resilience.BackoffPolicy
		attempt int
		want    time.Duration // with Rand returning its maximum
	}{
		{"default first attempt", resilience.DefaultBackoffPolicy(), 1, 2 * time.Second},
		{"default capped", resilience.DefaultBackoffPolicy(), 50, 5 * time.Minute},
		{"configured base", resilience.BackoffPolicy{Base: 100 * time.Millisecond, Cap: time.Second}, 3, 400 * time.Millisecond},
		{"configured cap", resilience.BackoffPolicy{Base: 100 * time.Millisecond, Cap: time.Second}, 5, time.Second},
		{"cap below next doubling", resilience.BackoffPolicy{Base: 3 * time.Second, Cap: 10 * time.Second}, 3, 10 * time.Second},
		{"huge cap does not overflow", resilience.BackoffPolicy{Base: time.Second, Cap: math.MaxInt64}, 200, math.MaxInt64 - 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotN int64
			tc.policy.Rand = func(n int64) int64 { gotN = n; return n - 1 }
			require.Equal(t, tc.want, tc.policy.Backoff(tc.attempt), "full jitter reaches the ceiling")
			require.LessOrEqual(t, tc.want, tc.policy.Ceiling(tc.attempt))
			require.Positive(t, gotN)

			tc.policy.Rand = func(int64) int64 { return 0 }
			require.Zero(t, tc.policy.Backoff(tc.attempt), "full jitter reaches zero")
		})
	}
}

func TestBackoffPolicy_DefaultRandStaysInBounds(t *testing.T) {
	p := resilience.DefaultBackoffPolicy()
	for attempt := 1; attempt <= 12; attempt++ {
		ceiling := p.Ceiling(attempt)
		for range 200 {
			d := p.Backoff(attempt)
			require.GreaterOrEqual(t, d, time.Duration(0))
			require.LessOrEqual(t, d, ceiling)
		}
	}
	for range 200 {
		require.LessOrEqual(t, resilience.Backoff(20), resilience.DefaultBackoffCap)
	}
}

func TestBackoffPolicy_Validate(t *testing.T) {
	cases := []struct {
		name   string
		policy resilience.BackoffPolicy
		ok     bool
	}{
		{"default", resilience.DefaultBackoffPolicy(), true},
		{"cap equals base", resilience.BackoffPolicy{Base: time.Second, Cap: time.Second}, true},
		{"zero base", resilience.BackoffPolicy{Cap: time.Second}, false},
		{"negative base", resilience.BackoffPolicy{Base: -time.Second, Cap: time.Second}, false},
		{"cap below base", resilience.BackoffPolicy{Base: 2 * time.Second, Cap: time.Second}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.ok {
				require.NoError(t, tc.policy.Validate())
			} else {
				require.Error(t, tc.policy.Validate())
			}
		})
	}
}
