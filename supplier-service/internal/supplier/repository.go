package supplier

import (
	"context"
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
// generates both IDs. Update keeps SupplierID
// stable, appends a fresh VersionID, and changes the current pointer without
// mutating an older snapshot. DeleteCurrent is a soft delete and never removes
// versions. DeleteCurrent atomically records its operation ID with the soft
// delete so an interrupted order-service fence commit can be reconciled.
type Writer interface {
	Create(context.Context, Details, time.Time) (Supplier, error)
	Update(context.Context, SupplierID, Patch, time.Time) (Supplier, error)
	DeleteCurrent(context.Context, SupplierID, DeletionOperationID, time.Time) error
}

// DeletionRecord is the durable local half of a guarded deletion. CompletedAt
// remains nil until order-service confirms that the fence is committed.
type DeletionRecord struct {
	OperationID DeletionOperationID
	SupplierID  SupplierID
	DeletedAt   time.Time
	CompletedAt *time.Time
}

// DeletionRecovery supports restart-safe reconciliation after the supplier was
// soft-deleted but the order-service commit response was lost. Implementations
// must return pending records in stable operation-ID order and bound the result
// by limit.
type DeletionRecovery interface {
	GetDeletion(context.Context, DeletionOperationID) (DeletionRecord, error)
	ListPendingDeletions(context.Context, int) ([]DeletionRecord, error)
	CompleteDeletion(context.Context, DeletionOperationID, time.Time) error
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
	DeletionRecovery
}
