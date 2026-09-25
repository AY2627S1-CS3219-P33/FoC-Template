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
// uniqueness enforcement. Create generates both IDs. Update keeps SupplierID
// stable, appends a fresh VersionID, and changes the current pointer without
// mutating an older snapshot. DeleteCurrent is a soft delete and never removes
// versions.
type Writer interface {
	Create(context.Context, Create, time.Time) (Supplier, error)
	Update(context.Context, SupplierID, Patch, time.Time) (Supplier, error)
	DeleteCurrent(context.Context, SupplierID, time.Time) error
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
}
