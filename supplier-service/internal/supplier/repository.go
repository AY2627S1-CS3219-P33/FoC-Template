package supplier

import (
	"context"
	"errors"
	"time"
)

// Reader serves current catalogue reads and immutable historical reads.
type Reader interface {
	ListAvailable(context.Context, ListFilter) (Page, error)
	GetCurrentAvailable(context.Context, SupplierID) (Supplier, error)
	GetVersion(context.Context, VersionID) (Version, error)
}

// Writer methods are atomic persistence operations, including normalized-name
// uniqueness enforcement. Create receives already validated details and
// generates both IDs. Update keeps SupplierID stable, appends a fresh
// VersionID, and changes the current pointer without mutating an older
// snapshot. Guarded deletion is exposed separately by DeletionStore.
type Writer interface {
	Create(context.Context, Details, time.Time) (Supplier, error)
	Update(context.Context, SupplierID, Patch, time.Time) (Supplier, error)
}

type DeletionState string

const (
	DeletionRequested             DeletionState = "requested"
	DeletionReleasePending        DeletionState = "release_pending"
	DeletionCommitPending         DeletionState = "commit_pending"
	DeletionRejectedActiveErrands DeletionState = "rejected_active_errands"
	DeletionCompleted             DeletionState = "completed"
)

// DeletionFailureCode is safe to persist and expose to operators. Dependency
// errors and credentials must never be stored in its place.
type DeletionFailureCode string

const (
	DeletionFailureAcquireUnavailable DeletionFailureCode = "acquire_unavailable"
	DeletionFailureAcquireAmbiguous   DeletionFailureCode = "acquire_ambiguous"
	DeletionFailureInspectUnavailable DeletionFailureCode = "inspect_unavailable"
	DeletionFailureReleaseUnavailable DeletionFailureCode = "release_unavailable"
	DeletionFailureCommitUnavailable  DeletionFailureCode = "commit_unavailable"
)

// DeletionClaimID identifies one worker's lease. It must be newly generated
// for each claim attempt and is not an authentication credential.
type DeletionClaimID string

var (
	// ErrDeletionOperationMismatch means an idempotency key is already bound to
	// another supplier. Callers map it to INVALID_ARGUMENT.
	ErrDeletionOperationMismatch = errors.New("deletion operation belongs to another supplier")
	// ErrDeletionClaimLost prevents an expired or superseded worker from
	// changing an operation.
	ErrDeletionClaimLost = errors.New("deletion operation claim lost")
	// ErrInvalidDeletionTransition means the requested state change is not
	// allowed by the deletion state machine.
	ErrInvalidDeletionTransition = errors.New("invalid deletion state transition")
)

// DeletionRecord is the durable local half of a guarded deletion. An operation
// is created in DeletionRequested before order-service Acquire is called.
type DeletionRecord struct {
	OperationID    DeletionOperationID
	SupplierID     SupplierID
	State          DeletionState
	AttemptCount   int
	MaxAttempts    int
	NextAttemptAt  *time.Time
	FailureCode    DeletionFailureCode
	ClaimID        DeletionClaimID
	LeaseExpiresAt *time.Time
	RequestedAt    time.Time
	UpdatedAt      time.Time
	DeletedAt      *time.Time
	CompletedAt    *time.Time
}

// DeletionStore owns the deletion state machine and restart-safe recovery.
//
// CreateOrGetDeletion must insert DeletionRequested durably before Acquire. An
// operation ID has a unique supplier mapping: the same pair returns the
// original record without changing its MaxAttempts, and another supplier
// returns ErrDeletionOperationMismatch. MaxAttempts must be positive for a new
// operation.
//
// ClaimDeletion atomically acquires an absent or expired lease, increments
// AttemptCount, and returns claimed=false when the operation is not due, is
// terminal, has an active lease, or has exhausted MaxAttempts. Every mutating
// method below requires the current ClaimID and returns ErrDeletionClaimLost
// for an expired or superseded claim. ListClaimableDeletions returns only due,
// non-terminal, non-exhausted records in stable operation-ID order and bounds
// its result by limit.
//
// Valid state transitions are:
//
//	requested -> release_pending -> rejected_active_errands
//	requested -> commit_pending  -> completed
//
// DeleteCurrentAndMarkCommitPending performs the supplier soft delete and
// state transition in one transaction without deleting immutable versions.
// ScheduleDeletionRetry retains the current state, stores only a safe failure
// code and next-attempt time, and releases the claim. A nil next-attempt time
// leaves an exhausted operation identifiable but not automatically runnable.
type DeletionStore interface {
	CreateOrGetDeletion(context.Context, DeletionOperationID, SupplierID, int, time.Time) (DeletionRecord, error)
	GetDeletion(context.Context, DeletionOperationID) (DeletionRecord, error)
	ListClaimableDeletions(context.Context, time.Time, int) ([]DeletionRecord, error)
	ClaimDeletion(context.Context, DeletionOperationID, DeletionClaimID, time.Time, time.Time) (DeletionRecord, bool, error)
	MarkDeletionReleasePending(context.Context, DeletionOperationID, DeletionClaimID, time.Time) error
	MarkDeletionRejectedActiveErrands(context.Context, DeletionOperationID, DeletionClaimID, time.Time) error
	DeleteCurrentAndMarkCommitPending(context.Context, SupplierID, DeletionOperationID, DeletionClaimID, time.Time) error
	CompleteDeletion(context.Context, DeletionOperationID, DeletionClaimID, time.Time) error
	ScheduleDeletionRetry(context.Context, DeletionOperationID, DeletionClaimID, DeletionFailureCode, *time.Time, time.Time) error
}

// NameUniqueness applies NormalizeName and excludes the supplied identity when
// checking a rename. Only non-deleted suppliers participate in uniqueness.
type NameUniqueness interface {
	NormalizedNameExists(context.Context, string, *SupplierID) (bool, error)
}

type Repository interface {
	Reader
	Writer
	NameUniqueness
	DeletionStore
}
