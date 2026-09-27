package deletefeature

import (
	"context"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
)

// Fence is the observable state of an order-service reservation plus the
// active-errand result recorded when creation of new errands was blocked.
type Fence struct {
	OperationID      supplier.DeletionOperationID
	State            FenceState
	HasActiveErrands bool
}

type FenceState string

const (
	FenceOpen      FenceState = "open"
	FenceCommitted FenceState = "committed"
	FenceReleased  FenceState = "released"
)

// OrderDeletionFence is the fail-closed deletion boundary with order-service.
//
// Acquire must be idempotent for OperationID: repeated calls with the same
// operation and supplier return the same fence, while reuse with another
// supplier fails. It atomically blocks new errands before checking for active
// errands. Inspect resolves an ambiguous Acquire or Commit result. The caller
// releases the fence if deletion is rejected or the supplier write fails.
// After a successful supplier soft delete, Commit makes the block permanent.
// Inspect, Commit, and Release must be idempotent. The implementation must
// never infer that there are no active errands from a dependency failure.
type OrderDeletionFence interface {
	Acquire(context.Context, supplier.DeletionOperationID, supplier.SupplierID) (Fence, error)
	Inspect(context.Context, supplier.DeletionOperationID) (Fence, error)
	Commit(context.Context, supplier.DeletionOperationID) error
	Release(context.Context, supplier.DeletionOperationID) error
}
