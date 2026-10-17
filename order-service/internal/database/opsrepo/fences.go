package opsrepo

import (
	"context"
	"errors"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/opsgen"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
)

// FenceState is the stored supplier_fences.state. The wire format is lower
// case; converting is the fence API's job (docs/contracts.md, gate C2).
type FenceState string

const (
	FenceOpen      FenceState = "OPEN"
	FenceCommitted FenceState = "COMMITTED"
	FenceReleased  FenceState = "RELEASED"
)

type Fence struct {
	OperationID string
	SupplierID  string
	State       FenceState
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// LockSupplier takes the exclusive advisory lock on the supplier's fence key
// (docs/contracts.md, "Fence Acquire" step 1). It only has an effect inside
// InTx; the lock is released when the transaction ends.
func (r *Repository) LockSupplier(ctx context.Context, supplierID string) error {
	id, err := parseUUID("supplierId", supplierID)
	if err != nil {
		return err
	}
	return mapError(r.queries.LockSupplierForFence(ctx, id))
}

// CreateFence creates an OPEN fence for the operation, or returns the
// existing one; created reports which. An existing fence for another
// supplier is returned with ErrFenceSupplierMismatch.
func (r *Repository) CreateFence(ctx context.Context, operationID, supplierID string) (fence Fence, created bool, err error) {
	opID, err := parseUUID("operationId", operationID)
	if err != nil {
		return Fence{}, false, err
	}
	supID, err := parseUUID("supplierId", supplierID)
	if err != nil {
		return Fence{}, false, err
	}
	inserted, err := r.queries.InsertFence(ctx, opsgen.InsertFenceParams{OperationID: opID, SupplierID: supID})
	if err != nil {
		return Fence{}, false, mapError(err)
	}
	row, err := r.queries.GetFence(ctx, opID)
	if err != nil {
		return Fence{}, false, mapError(err)
	}
	fence = fenceFromRow(row)
	if row.SupplierID != supID {
		return fence, false, ErrFenceSupplierMismatch
	}
	return fence, inserted == 1, nil
}

// GetFence returns ErrNotFound when no fence has the operation ID.
func (r *Repository) GetFence(ctx context.Context, operationID string) (Fence, error) {
	opID, err := parseUUID("operationId", operationID)
	if err != nil {
		return Fence{}, err
	}
	row, err := r.queries.GetFence(ctx, opID)
	if err != nil {
		return Fence{}, mapError(err)
	}
	return fenceFromRow(row), nil
}

// CloseFence moves an OPEN fence to state (COMMITTED or RELEASED). A fence
// already in state is returned unchanged, so replays are idempotent. A fence
// in the other terminal state is returned with ErrFenceStateConflict; a
// missing one gives ErrNotFound.
func (r *Repository) CloseFence(ctx context.Context, operationID string, state FenceState) (Fence, error) {
	if state != FenceCommitted && state != FenceReleased {
		return Fence{}, invalid("a fence can only be closed as %s or %s", FenceCommitted, FenceReleased)
	}
	opID, err := parseUUID("operationId", operationID)
	if err != nil {
		return Fence{}, err
	}
	row, err := r.queries.CloseFence(ctx, opsgen.CloseFenceParams{State: string(state), OperationID: opID})
	if err == nil {
		return fenceFromRow(row), nil
	}
	if mapped := mapError(err); !errors.Is(mapped, ErrNotFound) {
		return Fence{}, mapped
	}
	current, err := r.GetFence(ctx, operationID)
	if err != nil {
		return Fence{}, err
	}
	if current.State != state {
		return current, ErrFenceStateConflict
	}
	return current, nil
}

// CountActiveErrandsForSupplier counts errands in an active (non-terminal)
// state whose supplier, or whose pending edit's supplier, is supplierID
// (docs/contracts.md, "Fence Acquire" step 3).
func (r *Repository) CountActiveErrandsForSupplier(ctx context.Context, supplierID string) (int64, error) {
	id, err := parseUUID("supplierId", supplierID)
	if err != nil {
		return 0, err
	}
	count, err := r.queries.CountActiveErrandsForSupplier(ctx, opsgen.CountActiveErrandsForSupplierParams{
		ActiveStatuses: activeStatuses(),
		SupplierID:     id,
	})
	return count, mapError(err)
}

func activeStatuses() []string {
	var statuses []string
	for _, s := range errand.Statuses() {
		if s.Active() {
			statuses = append(statuses, string(s))
		}
	}
	return statuses
}

func fenceFromRow(row opsgen.SupplierFence) Fence {
	return Fence{
		OperationID: uuidString(row.OperationID),
		SupplierID:  uuidString(row.SupplierID),
		State:       FenceState(row.State),
		CreatedAt:   timeOrZero(row.CreatedAt),
		UpdatedAt:   timeOrZero(row.UpdatedAt),
	}
}
