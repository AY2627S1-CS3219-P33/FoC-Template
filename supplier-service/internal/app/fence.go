package app

import (
	"context"
	"errors"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
	deletefeature "github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier/delete"
)

// UnavailableDeletionFence keeps the registered deletion workflow fail-closed
// until the B4 order-service adapter can be supplied at the composition root.
// It must never pretend an active-errand check succeeded.
type UnavailableDeletionFence struct{}

var errFenceUnavailable = errors.New("order-service deletion fence is not configured")

var _ deletefeature.OrderDeletionFence = UnavailableDeletionFence{}

func (UnavailableDeletionFence) Acquire(context.Context, supplier.DeletionOperationID, supplier.SupplierID) (deletefeature.Fence, error) {
	return deletefeature.Fence{}, errFenceUnavailable
}

func (UnavailableDeletionFence) Inspect(context.Context, supplier.DeletionOperationID) (deletefeature.Fence, error) {
	return deletefeature.Fence{}, errFenceUnavailable
}

func (UnavailableDeletionFence) Commit(context.Context, supplier.DeletionOperationID) error {
	return errFenceUnavailable
}

func (UnavailableDeletionFence) Release(context.Context, supplier.DeletionOperationID) error {
	return errFenceUnavailable
}
