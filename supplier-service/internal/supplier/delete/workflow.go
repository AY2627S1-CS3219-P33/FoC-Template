package deletefeature

import (
	"context"
	"strings"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
)

// Outcome is the result of advancing one claimed deletion operation. The
// handler maps it to a response; the reconciler only logs it.
type Outcome string

const (
	OutcomeCompleted             Outcome = "completed"
	OutcomeRejectedActiveErrands Outcome = "rejected_active_errands"
	OutcomeCommitPending         Outcome = "commit_pending"
	OutcomeFenceUnavailable      Outcome = "fence_unavailable"
	OutcomeReleaseUnavailable    Outcome = "release_unavailable"
	// OutcomeFenceReleased means order-service already released this
	// operation's fence, so it can never authorise a deletion. The caller
	// must start a new operation with a new Idempotency-Key.
	OutcomeFenceReleased    Outcome = "fence_released"
	OutcomeSupplierNotFound Outcome = "supplier_not_found"
	OutcomeInternal         Outcome = "internal_error"
)

// workflow advances a claimed operation through the state machine in
// docs/contracts.md ("Order-service deletion fence"). The handler and the
// reconciler share it, so a request and a background retry of the same
// operation follow identical rules. Every method requires the caller to hold
// the operation's claim lease.
type workflow struct {
	store        supplier.DeletionStore
	fence        OrderDeletionFence
	retryBackoff time.Duration
}

func (w workflow) advance(ctx context.Context, record supplier.DeletionRecord, claimID supplier.DeletionClaimID, now time.Time) Outcome {
	switch record.State {
	case supplier.DeletionRequested:
		return w.acquire(ctx, record, claimID, now)
	case supplier.DeletionReleasePending:
		return w.release(ctx, record, claimID, now)
	case supplier.DeletionCommitPending:
		return w.commit(ctx, record, claimID, now)
	default:
		return OutcomeInternal
	}
}

func (w workflow) acquire(ctx context.Context, record supplier.DeletionRecord, claimID supplier.DeletionClaimID, now time.Time) Outcome {
	fence, err := w.fence.Acquire(ctx, record.OperationID, record.SupplierID)
	if err != nil {
		// A failed Acquire may still have committed; only Inspect can tell.
		var inspErr error
		fence, inspErr = w.fence.Inspect(ctx, record.OperationID)
		if inspErr != nil {
			w.scheduleRetry(ctx, record, claimID, supplier.DeletionFailureAcquireUnavailable, now)
			return OutcomeFenceUnavailable
		}
	}
	// Only a fence bound to this supplier blocks errand creation for it.
	if !strings.EqualFold(string(fence.SupplierID), string(record.SupplierID)) {
		w.scheduleRetry(ctx, record, claimID, supplier.DeletionFailureAcquireAmbiguous, now)
		return OutcomeFenceUnavailable
	}

	switch fence.State {
	case FenceOpen:
		if fence.HasActiveErrands {
			if err := w.store.MarkDeletionReleasePending(ctx, record.OperationID, claimID, now); err != nil {
				return OutcomeInternal
			}
			return w.release(ctx, record, claimID, now)
		}
	case FenceCommitted:
		// An earlier attempt got as far as Commit; finish the same way.
	case FenceReleased:
		// An earlier attempt released the fence after its supplier write
		// failed. A released fence cannot be reacquired under this
		// operation ID, and deleting without it would leave errand creation
		// unblocked, so stop retrying this operation.
		w.stopRetrying(ctx, record, claimID, now)
		return OutcomeFenceReleased
	default:
		w.scheduleRetry(ctx, record, claimID, supplier.DeletionFailureAcquireAmbiguous, now)
		return OutcomeFenceUnavailable
	}

	if err := w.store.DeleteCurrentAndMarkCommitPending(ctx, record.SupplierID, record.OperationID, claimID, now); err != nil {
		// The supplier was not deleted, so lift the block on errand
		// creation. Only release while this claim still owns a requested
		// operation: if the lease was lost, another worker may already have
		// deleted the supplier and depend on the fence staying closed. A
		// failed Release leaves the fence closed, which is safe.
		if current, getErr := w.store.GetDeletion(ctx, record.OperationID); getErr == nil &&
			current.State == supplier.DeletionRequested && current.ClaimID == claimID {
			_ = w.fence.Release(ctx, record.OperationID)
		}
		if isSupplierNotFound(err) {
			return OutcomeSupplierNotFound
		}
		return OutcomeInternal
	}
	return w.commit(ctx, record, claimID, now)
}

func (w workflow) release(ctx context.Context, record supplier.DeletionRecord, claimID supplier.DeletionClaimID, now time.Time) Outcome {
	if err := w.fence.Release(ctx, record.OperationID); err != nil {
		w.scheduleRetry(ctx, record, claimID, supplier.DeletionFailureReleaseUnavailable, now)
		return OutcomeReleaseUnavailable
	}
	if err := w.store.MarkDeletionRejectedActiveErrands(ctx, record.OperationID, claimID, now); err != nil {
		return OutcomeInternal
	}
	return OutcomeRejectedActiveErrands
}

// commit runs once the supplier is soft-deleted. It never releases the
// fence: an unconfirmed Commit stays commit_pending and is retried.
func (w workflow) commit(ctx context.Context, record supplier.DeletionRecord, claimID supplier.DeletionClaimID, now time.Time) Outcome {
	if err := w.fence.Commit(ctx, record.OperationID); err != nil {
		if fence, inspErr := w.fence.Inspect(ctx, record.OperationID); inspErr != nil || fence.State != FenceCommitted {
			w.scheduleRetry(ctx, record, claimID, supplier.DeletionFailureCommitUnavailable, now)
			return OutcomeCommitPending
		}
	}
	if err := w.store.CompleteDeletion(ctx, record.OperationID, claimID, now); err != nil {
		return OutcomeInternal
	}
	return OutcomeCompleted
}

// scheduleRetry releases the claim and makes the operation due again after
// the backoff, or never once its attempts are exhausted. A failure to
// record the retry leaves the lease to expire, which has the same effect.
func (w workflow) scheduleRetry(ctx context.Context, record supplier.DeletionRecord, claimID supplier.DeletionClaimID, code supplier.DeletionFailureCode, now time.Time) {
	var next *time.Time
	if record.AttemptCount < record.MaxAttempts {
		t := now.Add(w.retryBackoff)
		next = &t
	}
	_ = w.store.ScheduleDeletionRetry(ctx, record.OperationID, claimID, code, next, now)
}

func (w workflow) stopRetrying(ctx context.Context, record supplier.DeletionRecord, claimID supplier.DeletionClaimID, now time.Time) {
	_ = w.store.ScheduleDeletionRetry(ctx, record.OperationID, claimID, supplier.DeletionFailureAcquireAmbiguous, nil, now)
}
