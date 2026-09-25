package deletefeature

import (
	"context"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
)

// Fence is an opaque order-service reservation plus the active-errand result
// observed while creation of new errands for the supplier is blocked.
type Fence struct {
	ID               string
	HasActiveErrands bool
}

// OrderDeletionFence is the fail-closed deletion boundary with order-service.
//
// Acquire must atomically block new errands for SupplierID before checking for
// active errands. The caller releases the fence if deletion is rejected or the
// supplier write fails. After a successful supplier soft delete, Commit makes
// the block permanent. Commit and Release must be idempotent. Any unavailable
// or ambiguous result rejects deletion; the implementation must never infer
// that there are no active errands from a dependency failure.
type OrderDeletionFence interface {
	Acquire(context.Context, supplier.SupplierID) (Fence, error)
	Commit(context.Context, string) error
	Release(context.Context, string) error
}
