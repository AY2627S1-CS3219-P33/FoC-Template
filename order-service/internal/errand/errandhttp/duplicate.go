package errandhttp

import (
	"context"
	"errors"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

// LostToDuplicate reports whether a Store.Apply that failed with err lost
// its compare-and-swap to a duplicate of the same request: a double-tapped
// button, or a client retry sent before the first response arrived. That is
// when err is ERRAND_CHANGED or ERRAND_UNAVAILABLE and the errand, read
// again, already shows the request's effect (done, the handler's retry
// check). The handler then answers 200 with the returned errand, as it
// answers a retry that arrives after the commit, instead of a 409 and a
// rejection event for a request that took effect (F3.10.2).
//
// A failed re-read is reported as not lost, so the caller writes the
// original refusal.
func LostToDuplicate(ctx context.Context, store errand.Store, errandID string, err error, done func(errand.Errand) bool) (errand.Errand, bool) {
	var appErr *apperror.Error
	if !errors.As(err, &appErr) || (appErr.Code != apperror.ErrandChanged && appErr.Code != apperror.ErrandUnavailable) {
		return errand.Errand{}, false
	}
	current, getErr := store.Get(ctx, errandID)
	if getErr != nil || !done(current) {
		return errand.Errand{}, false
	}
	return current, true
}
