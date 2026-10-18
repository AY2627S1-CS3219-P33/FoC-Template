package resilience_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/resilience"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/testfakes"
)

var (
	t0           = time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	errBoom      = errors.New("boom")
	errNotFound  = errors.New("not found")
	testCoolDown = 30 * time.Second
)

func newBreaker(t *testing.T, threshold int) (*resilience.Breaker, *testfakes.FakeClock) {
	t.Helper()
	clock := testfakes.NewFakeClock(t0)
	b, err := resilience.NewBreaker(resilience.BreakerConfig{
		Name:             "dep",
		FailureThreshold: threshold,
		CoolDown:         testCoolDown,
		Clock:            clock,
		IsFailure:        func(err error) bool { return !errors.Is(err, errNotFound) },
	})
	require.NoError(t, err)
	return b, clock
}

// step is one action in a breaker scenario.
type step struct {
	call      error         // result fn returns; ignored when advance is set
	cancelled bool          // the caller's context is cancelled before fn returns
	advance   time.Duration // move the clock instead of calling
	wantOpen  bool          // Do returns ErrCircuitOpen without calling fn
	wantState resilience.State
}

func ok() step                  { return step{call: nil} }
func fail() step                { return step{call: errBoom} }
func wait(d time.Duration) step { return step{advance: d} }

func (s step) then(state resilience.State) step { s.wantState = state; return s }
func (s step) rejected() step                   { s.wantOpen = true; return s }

func TestBreaker_Transitions(t *testing.T) {
	closed, open, half := resilience.StateClosed, resilience.StateOpen, resilience.StateHalfOpen
	cases := []struct {
		name  string
		steps []step
	}{
		{"closed stays closed below threshold", []step{
			fail().then(closed), fail().then(closed),
		}},
		{"closed opens at threshold", []step{
			fail().then(closed), fail().then(closed), fail().then(open),
		}},
		{"success resets the consecutive count", []step{
			fail().then(closed), fail().then(closed), ok().then(closed),
			fail().then(closed), fail().then(closed), fail().then(open),
		}},
		{"non-failure errors do not count", []step{
			fail().then(closed), fail().then(closed),
			{call: errNotFound, wantState: closed}, {call: errNotFound, wantState: closed},
			fail().then(open),
		}},
		{"caller cancellation does not count", []step{
			fail().then(closed), fail().then(closed),
			{call: context.Canceled, cancelled: true, wantState: closed},
			fail().then(open),
		}},
		{"open rejects without calling fn", []step{
			fail().then(closed), fail().then(closed), fail().then(open),
			ok().rejected().then(open), fail().rejected().then(open),
		}},
		{"open stays open until the cool-down has elapsed", []step{
			fail().then(closed), fail().then(closed), fail().then(open),
			wait(testCoolDown - time.Nanosecond).then(open),
			ok().rejected().then(open),
		}},
		{"open becomes half-open after the cool-down", []step{
			fail().then(closed), fail().then(closed), fail().then(open),
			wait(testCoolDown).then(half),
		}},
		{"half-open trial success closes", []step{
			fail().then(closed), fail().then(closed), fail().then(open), wait(testCoolDown).then(half),
			ok().then(closed),
			fail().then(closed), fail().then(closed), fail().then(open),
		}},
		{"half-open trial failure reopens and restarts the cool-down", []step{
			fail().then(closed), fail().then(closed), fail().then(open), wait(testCoolDown).then(half),
			fail().then(open),
			wait(testCoolDown - time.Nanosecond).then(open),
			ok().rejected().then(open),
			wait(time.Nanosecond).then(half),
			ok().then(closed),
		}},
		{"half-open trial cancelled by the caller stays half-open", []step{
			fail().then(closed), fail().then(closed), fail().then(open), wait(testCoolDown).then(half),
			{call: context.Canceled, cancelled: true, wantState: half},
			ok().then(closed),
		}},
		{"half-open trial non-failure error stays half-open", []step{
			fail().then(closed), fail().then(closed), fail().then(open), wait(testCoolDown).then(half),
			{call: errNotFound, wantState: half},
			fail().then(open),
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, clock := newBreaker(t, 3)
			for i, s := range tc.steps {
				if s.advance > 0 {
					clock.Advance(s.advance)
				} else {
					ctx, cancel := context.WithCancel(context.Background())
					called := false
					err := resilience.Do(ctx, b, time.Second, func(context.Context) error {
						called = true
						if s.cancelled {
							cancel()
						}
						return s.call
					})
					cancel()
					if s.wantOpen {
						require.ErrorIs(t, err, resilience.ErrCircuitOpen, "step %d", i)
						require.False(t, called, "step %d: fn must not run while open", i)
					} else {
						require.True(t, called, "step %d", i)
						require.Equal(t, s.call, err, "step %d: Do returns fn's error unchanged", i)
					}
				}
				require.Equal(t, s.wantState, b.State(), "step %d", i)
			}
		})
	}
}

func TestBreaker_HalfOpenAllowsExactlyOneTrial(t *testing.T) {
	b, clock := newBreaker(t, 1)
	require.Error(t, resilience.Do(context.Background(), b, time.Second, func(context.Context) error { return errBoom }))
	clock.Advance(testCoolDown)
	require.Equal(t, resilience.StateHalfOpen, b.State())

	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	trialDone := make(chan error)
	go func() {
		trialDone <- resilience.Do(context.Background(), b, time.Minute, func(context.Context) error {
			calls.Add(1)
			close(started)
			<-release
			return nil
		})
	}()
	<-started

	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			err := resilience.Do(context.Background(), b, time.Minute, func(context.Context) error {
				calls.Add(1)
				return nil
			})
			assert.ErrorIs(t, err, resilience.ErrCircuitOpen)
		})
	}
	wg.Wait()
	require.Equal(t, resilience.StateHalfOpen, b.State())

	close(release)
	require.NoError(t, <-trialDone)
	require.Equal(t, int32(1), calls.Load())
	require.Equal(t, resilience.StateClosed, b.State())
}

func TestBreaker_IgnoresOutcomeOfCallsFromAnEarlierState(t *testing.T) {
	b, _ := newBreaker(t, 2)
	started, release := make(chan struct{}), make(chan struct{})
	slowDone := make(chan error)
	go func() {
		slowDone <- resilience.Do(context.Background(), b, time.Minute, func(context.Context) error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started

	for range 2 {
		require.ErrorIs(t, resilience.Do(context.Background(), b, time.Second, func(context.Context) error { return errBoom }), errBoom)
	}
	require.Equal(t, resilience.StateOpen, b.State())

	close(release)
	require.NoError(t, <-slowDone)
	require.Equal(t, resilience.StateOpen, b.State(), "a success admitted while closed must not close an open breaker")
}

func TestBreaker_ConcurrentUse(t *testing.T) {
	b, clock := newBreaker(t, 3)
	var wg sync.WaitGroup
	for i := range 200 {
		wg.Go(func() {
			_ = resilience.Do(context.Background(), b, time.Second, func(context.Context) error {
				if i%3 == 0 {
					return errBoom
				}
				return nil
			})
			_ = b.State()
			if i%20 == 0 {
				clock.Advance(testCoolDown)
			}
		})
	}
	wg.Wait()
}

func TestBreaker_PanicCountsAsFailure(t *testing.T) {
	b, _ := newBreaker(t, 1)
	require.PanicsWithValue(t, "kaboom", func() {
		_ = resilience.Do(context.Background(), b, time.Second, func(context.Context) error { panic("kaboom") })
	})
	require.Equal(t, resilience.StateOpen, b.State())
}

func TestBreaker_String(t *testing.T) {
	require.Equal(t, "closed", resilience.StateClosed.String())
	require.Equal(t, "open", resilience.StateOpen.String())
	require.Equal(t, "half-open", resilience.StateHalfOpen.String())
	require.Equal(t, "State(9)", resilience.State(9).String())

	b, _ := newBreaker(t, 1)
	require.Equal(t, "dep", b.Name())
	require.Equal(t, "dep: closed", b.String())
	_ = resilience.Do(context.Background(), b, time.Second, func(context.Context) error { return errBoom })
	require.Equal(t, "dep: open", b.String())
}

func TestBreakerConfig_Validate(t *testing.T) {
	cases := []struct {
		name string
		edit func(*resilience.BreakerConfig)
		ok   bool
	}{
		{"default", func(*resilience.BreakerConfig) {}, true},
		{"missing name", func(c *resilience.BreakerConfig) { c.Name = "" }, false},
		{"zero threshold", func(c *resilience.BreakerConfig) { c.FailureThreshold = 0 }, false},
		{"zero cool-down", func(c *resilience.BreakerConfig) { c.CoolDown = 0 }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := resilience.DefaultBreakerConfig("dep")
			tc.edit(&cfg)
			b, err := resilience.NewBreaker(cfg)
			if tc.ok {
				require.NoError(t, err)
				require.Equal(t, resilience.StateClosed, b.State())
			} else {
				require.Error(t, err)
			}
		})
	}
}
