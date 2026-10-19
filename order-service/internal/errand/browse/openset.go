package browse

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand/errandhttp"
)

const (
	// OpenSetTTL is how long the open set is cached: the list may be up to
	// this stale (docs/contracts.md, "Cache contract"). Acceptance is still
	// checked against the current version (A4).
	OpenSetTTL = 2 * time.Second
	// loadTimeout bounds one shared load, which no single request can cancel.
	loadTimeout = 10 * time.Second
)

// openSet loads every open errand as errand.OpenErrandView: from the cache,
// or on a miss from the store behind a singleflight group, so concurrent
// misses cause one load. The result is shared between requests and must not
// be modified.
type openSet struct {
	store     errand.Store
	suppliers errand.SupplierGate
	cache     errand.OpenErrandCache
	clock     errand.Clock
	logger    *slog.Logger
	loads     *singleflight.Group
}

func (s openSet) get(ctx context.Context) ([]errand.OpenErrandView, error) {
	if views, ok := s.cache.Get(ctx); ok {
		return views, nil
	}
	result := s.loads.DoChan("open", func() (any, error) {
		// The load is shared: one caller giving up must not fail the others.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), loadTimeout)
		defer cancel()
		views, err := s.load(ctx)
		if err != nil {
			return nil, err
		}
		s.cache.Set(ctx, views, OpenSetTTL)
		return views, nil
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-result:
		if r.Err != nil {
			return nil, r.Err
		}
		return r.Val.([]errand.OpenErrandView), nil
	}
}

// load reads every OPEN errand whose acceptance deadline is after now, page
// by page, and projects each onto its supplier version's pickup location.
func (s openSet) load(ctx context.Context) ([]errand.OpenErrandView, error) {
	now := s.clock.Now()
	var errands []errand.Errand
	q := errand.OpenQuery{Now: now, Limit: errand.MaxListLimit}
	for {
		page, err := s.store.ListOpen(ctx, q)
		if err != nil {
			return nil, err
		}
		errands = append(errands, page.Items...)
		if page.Next == nil {
			break
		}
		q.After = page.Next
	}

	versions := map[string]errand.SupplierVersion{}
	unknown := map[string]bool{}
	views := make([]errand.OpenErrandView, 0, len(errands))
	for _, e := range errands {
		id := e.Details.SupplierVersionID
		if unknown[id] {
			continue
		}
		v, ok := versions[id]
		if !ok {
			var err error
			if v, err = s.suppliers.GetVersion(ctx, id); err != nil {
				if !hasCode(err, apperror.SupplierUnavailable) {
					return nil, err
				}
				// Versions are kept for deleted suppliers, so this is bad
				// data. Hiding the errands beats failing the list for all.
				s.logger.WarnContext(ctx, "open errand has an unknown supplier version",
					slog.String("errand_id", e.ID), slog.String("supplier_version_id", id))
				unknown[id] = true
				continue
			}
			versions[id] = v
		}
		views = append(views, errandhttp.OpenViewOf(e, v))
	}
	return views, nil
}

func hasCode(err error, code apperror.Code) bool {
	var appErr *apperror.Error
	return errors.As(err, &appErr) && appErr.Code == code
}
