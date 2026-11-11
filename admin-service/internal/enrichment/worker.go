// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-09
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

// Package enrichment fills in each errand's supplier and delivery building.
//
// The events carry neither, so after an errand's accepted event, and after
// every terminal event, admin-service asks order-service's export for them
// (edits can change both while an errand is OPEN and cannot after it is
// accepted). The ask is by errand ID, in batches. When order-service is down,
// or supplier-service was down for it so the supplier's name comes back null,
// the errand stays due and is retried later, never blocking the event stream.
package enrichment

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/facts"
	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/orderclient"
)

// Store is the read model's enrichment side; *facts.Store implements it.
type Store interface {
	EnrichmentDue(ctx context.Context, retryBefore time.Time, limit int) ([]string, error)
	StoreEnrichment(ctx context.Context, e facts.Enrichment, at time.Time) (bool, error)
	MarkEnrichmentTried(ctx context.Context, errandID string, at time.Time) error
}

var _ Store = (*facts.Store)(nil)

// Source is order-service's errand export; *orderclient.Client implements it.
type Source interface {
	ErrandsByID(ctx context.Context, ids []string) ([]orderclient.Errand, error)
}

var _ Source = (*orderclient.Client)(nil)

// Config tunes the worker. Zero fields take the defaults.
type Config struct {
	// Interval is how often due errands are looked for when nothing wakes
	// the worker, and the pause after a failed pass. Default 30 s.
	Interval time.Duration
	// RetryAfter is how long after a try an errand that is still due
	// (supplier name unknown) is tried again. Default 5 min.
	RetryAfter time.Duration
	// Batch is how many errands one pass asks for, at most the export's
	// limit of 100. Default 100.
	Batch int
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Worker enriches errands that owe it.
type Worker struct {
	store  Store
	source Source
	logger *slog.Logger
	cfg    Config
	wake   chan struct{}
}

func NewWorker(store Store, source Source, logger *slog.Logger, cfg Config) (*Worker, error) {
	if store == nil || source == nil || logger == nil {
		return nil, errors.New("enrichment: worker dependencies are required")
	}
	if cfg.Interval == 0 {
		cfg.Interval = 30 * time.Second
	}
	if cfg.RetryAfter == 0 {
		cfg.RetryAfter = 5 * time.Minute
	}
	if cfg.Batch == 0 {
		cfg.Batch = orderclient.MaxIDs
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Interval < 0 || cfg.RetryAfter < 0 || cfg.Batch < 1 || cfg.Batch > orderclient.MaxIDs {
		return nil, errors.New("enrichment: interval, retry and batch must be positive, the batch at most 100")
	}
	return &Worker{store: store, source: source, logger: logger, cfg: cfg, wake: make(chan struct{}, 1)}, nil
}

// Wake asks for a pass soon. It never blocks, and wakes coalesce.
func (w *Worker) Wake() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Run enriches until ctx is cancelled, then returns ctx's error. A pass runs
// at start, when woken, and every Interval; a failed pass waits an Interval
// before the next, however often it is woken.
func (w *Worker) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.cfg.Interval)
	defer ticker.Stop()
	for {
		if err := w.drain(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			w.logger.WarnContext(ctx, "enrichment pass failed; retrying later", slog.Any("error", err))
			timer := time.NewTimer(w.cfg.Interval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-w.wake:
		case <-ticker.C:
		}
	}
}

// drain runs passes until one finds nothing new to do.
func (w *Worker) drain(ctx context.Context) error {
	for {
		tried, err := w.Pass(ctx)
		if err != nil || tried < w.cfg.Batch {
			return err
		}
	}
}

// Pass enriches one batch of due errands and returns how many it asked for.
func (w *Worker) Pass(ctx context.Context) (int, error) {
	now := w.cfg.Now()
	ids, err := w.store.EnrichmentDue(ctx, now.Add(-w.cfg.RetryAfter), w.cfg.Batch)
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	errands, err := w.source.ErrandsByID(ctx, ids)
	if err != nil {
		return 0, err
	}
	for _, e := range errands {
		enrichment := facts.Enrichment{
			ErrandID:           e.ErrandID,
			SupplierID:         e.SupplierID,
			SupplierName:       e.SupplierName,
			DeliveryBuildingID: e.DeliveryBuildingID, DeliveryBuildingName: e.DeliveryBuildingName,
		}
		if _, err := w.store.StoreEnrichment(ctx, enrichment, now); err != nil {
			return 0, err
		}
	}
	// Errands order-service did not return stay due: a later pass asks again.
	// Mark the ones asked for as tried, so a pass never repeats itself at once.
	for _, id := range ids {
		if !returned(errands, id) {
			if err := w.store.MarkEnrichmentTried(ctx, id, now); err != nil {
				return 0, err
			}
		}
	}
	return len(ids), nil
}

func returned(errands []orderclient.Errand, id string) bool {
	for _, e := range errands {
		if e.ErrandID == id {
			return true
		}
	}
	return false
}
