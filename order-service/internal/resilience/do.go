package resilience

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrTimeout wraps the error of a call that ran past Do's per-call timeout.
// The wrapped error still matches context.DeadlineExceeded when fn returned
// it.
var ErrTimeout = errors.New("resilience: call timed out")

// Do runs fn once through breaker under a per-call timeout and records the
// outcome. It never retries.
//
//   - While the breaker is open (or half-open with its trial in flight), Do
//     returns ErrCircuitOpen without calling fn.
//   - fn receives a context that ends after timeout; timeout <= 0 means only
//     ctx's own deadline applies. fn must honour its context: Do waits for fn
//     to return and does not abandon it in a goroutine.
//   - If ctx itself is cancelled or past its deadline when fn returns, the
//     outcome is ignored: the caller gave up, which says nothing about the
//     dependency's health.
//   - If the per-call timeout fired, the call is a failure and the returned
//     error wraps ErrTimeout.
//   - Any other error is a failure unless the breaker's IsFailure says
//     otherwise; nil is a success. A panic in fn counts as a failure and is
//     re-raised.
func Do(ctx context.Context, breaker *Breaker, timeout time.Duration, fn func(context.Context) error) (err error) {
	t, err := breaker.allow()
	if err != nil {
		return err
	}

	callCtx, cancel := ctx, context.CancelFunc(func() {})
	if timeout > 0 {
		callCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	recorded := false
	defer func() {
		if !recorded {
			breaker.record(t, outcomeFailure) // fn panicked
		}
	}()
	err = fn(callCtx)
	recorded = true

	switch {
	case err == nil:
		breaker.record(t, outcomeSuccess)
		return nil
	case ctx.Err() != nil:
		breaker.record(t, outcomeIgnored)
		return err
	case callCtx.Err() != nil:
		breaker.record(t, outcomeFailure)
		return fmt.Errorf("%w after %s: %w", ErrTimeout, timeout, err)
	case breaker.isFailure(err):
		breaker.record(t, outcomeFailure)
		return err
	default:
		breaker.record(t, outcomeIgnored)
		return err
	}
}
