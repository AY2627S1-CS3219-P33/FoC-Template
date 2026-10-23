package deletefeature

import (
	"context"
	"log/slog"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
)

// reconcileBatchSize is the most operations one pass claims; it matches the
// repository's page limit.
const reconcileBatchSize = 100

// Reconciler resumes deletion operations that a request left unfinished
// (B6, NFR7.4): an unconfirmed Commit, a Release that failed after active
// errands were found, or an Acquire that could not be resolved. It lists
// operations that are due, claims each with its own lease, and advances it
// with the same workflow as the handler, so it is safe to run alongside
// requests and in several replicas.
type Reconciler struct {
	store         supplier.DeletionStore
	workflow      workflow
	interval      time.Duration
	leaseDuration time.Duration
	logger        *slog.Logger
	now           func() time.Time
	newClaimID    func() supplier.DeletionClaimID
}

func NewReconciler(store supplier.DeletionStore, fence OrderDeletionFence, interval time.Duration, logger *slog.Logger) Reconciler {
	if store == nil {
		panic("delete: store is required")
	}
	if fence == nil {
		panic("delete: fence is required")
	}
	if interval <= 0 {
		panic("delete: reconcile interval must be positive")
	}
	if logger == nil {
		panic("delete: logger is required")
	}
	return Reconciler{
		store:         store,
		workflow:      workflow{store: store, fence: fence, retryBackoff: defaultRetryBackoff},
		interval:      interval,
		leaseDuration: defaultLeaseDuration,
		logger:        logger,
		now:           func() time.Time { return time.Now().UTC() },
		newClaimID:    defaultClaimID,
	}
}

// Run reconciles once immediately, so operations interrupted by a restart
// resume at startup, and then every interval until ctx ends.
func (r Reconciler) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		r.ReconcileOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// ReconcileOnce advances every operation that is due now and returns how
// many it claimed. An operation claimed elsewhere in the meantime is skipped.
func (r Reconciler) ReconcileOnce(ctx context.Context) int {
	records, err := r.store.ListClaimableDeletions(ctx, r.now(), reconcileBatchSize)
	if err != nil {
		if ctx.Err() == nil {
			r.logger.ErrorContext(ctx, "list pending supplier deletions failed", slog.Any("error", err))
		}
		return 0
	}
	claimed := 0
	for _, record := range records {
		if ctx.Err() != nil {
			break
		}
		// Each operation gets a fresh clock so its lease is measured from
		// when it was claimed, not from when the pass began.
		now := r.now()
		claimID := r.newClaimID()
		current, ok, err := r.store.ClaimDeletion(ctx, record.OperationID, claimID, now, now.Add(r.leaseDuration))
		if err != nil {
			r.logger.ErrorContext(ctx, "claim supplier deletion failed",
				slog.String("operation_id", string(record.OperationID)), slog.Any("error", err))
			continue
		}
		if !ok {
			continue
		}
		claimed++
		outcome := r.workflow.advance(ctx, current, claimID, now)
		attrs := []any{
			slog.String("operation_id", string(current.OperationID)),
			slog.String("supplier_id", string(current.SupplierID)),
			slog.String("from_state", string(current.State)),
			slog.String("outcome", string(outcome)),
			slog.Int("attempt", current.AttemptCount),
			slog.Int("max_attempts", current.MaxAttempts),
		}
		switch outcome {
		case OutcomeCompleted, OutcomeRejectedActiveErrands:
			r.logger.InfoContext(ctx, "supplier deletion reconciled", attrs...)
		case OutcomeInternal, OutcomeFenceReleased:
			r.logger.ErrorContext(ctx, "supplier deletion reconcile failed", attrs...)
		default:
			r.logger.WarnContext(ctx, "supplier deletion still pending", attrs...)
		}
	}
	return claimed
}
