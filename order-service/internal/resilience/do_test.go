package resilience_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/resilience"
)

func TestDo_Timeouts(t *testing.T) {
	waitForCtx := func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}
	cases := []struct {
		name        string
		parent      func() (context.Context, context.CancelFunc)
		timeout     time.Duration
		wantTimeout bool // error wraps ErrTimeout
		wantState   resilience.State
	}{
		{
			name:        "per-call timeout is a failure",
			parent:      func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) },
			timeout:     10 * time.Millisecond,
			wantTimeout: true,
			wantState:   resilience.StateOpen,
		},
		{
			name: "caller cancellation is not a failure",
			parent: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				time.AfterFunc(10*time.Millisecond, cancel)
				return ctx, cancel
			},
			timeout:   time.Minute,
			wantState: resilience.StateClosed,
		},
		{
			name: "caller deadline shorter than the timeout is not a failure",
			parent: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 10*time.Millisecond)
			},
			timeout:   time.Minute,
			wantState: resilience.StateClosed,
		},
		{
			name: "no per-call timeout leaves only the caller deadline",
			parent: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 10*time.Millisecond)
			},
			timeout:   0,
			wantState: resilience.StateClosed,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := newBreaker(t, 1)
			ctx, cancel := tc.parent()
			defer cancel()
			err := resilience.Do(ctx, b, tc.timeout, waitForCtx)
			require.Error(t, err)
			if tc.wantTimeout {
				require.ErrorIs(t, err, resilience.ErrTimeout)
				require.ErrorIs(t, err, context.DeadlineExceeded)
			} else {
				require.NotErrorIs(t, err, resilience.ErrTimeout)
			}
			require.Equal(t, tc.wantState, b.State())
		})
	}
}

func TestDo_PassesDeadlineToFn(t *testing.T) {
	b, _ := newBreaker(t, 1)
	before := time.Now()
	require.NoError(t, resilience.Do(context.Background(), b, time.Minute, func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.WithinDuration(t, before.Add(time.Minute), deadline, 5*time.Second)
		return nil
	}))
	require.NoError(t, resilience.Do(context.Background(), b, 0, func(ctx context.Context) error {
		_, ok := ctx.Deadline()
		require.False(t, ok, "timeout <= 0 adds no deadline")
		return nil
	}))
}

func TestDo_FastCallWithinTimeoutSucceeds(t *testing.T) {
	b, _ := newBreaker(t, 1)
	require.NoError(t, resilience.Do(context.Background(), b, time.Second, func(context.Context) error { return nil }))
	require.Equal(t, resilience.StateClosed, b.State())
}
