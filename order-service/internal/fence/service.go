package fence

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/opsrepo"
)

// State is the wire form of a fence's state, matching supplier-service's
// deletefeature.FenceState. The supplier_fences column stores upper case.
type State string

const (
	StateOpen      State = "open"
	StateCommitted State = "committed"
	StateReleased  State = "released"
)

// Fence is the body every fence route returns.
type Fence struct {
	OperationID      string
	SupplierID       string
	State            State
	HasActiveErrands bool
}

// Fences is what the HTTP handler needs. actor is the calling service's sub
// and is recorded in the audit log. A refused request returns an
// *apperror.Error; any other error is an internal failure.
type Fences interface {
	Acquire(ctx context.Context, actor, operationID, supplierID string) (Fence, error)
	Inspect(ctx context.Context, operationID string) (Fence, error)
	Commit(ctx context.Context, actor, operationID string) (Fence, error)
	Release(ctx context.Context, actor, operationID string) (Fence, error)
}

// Audit log vocabulary for fence actions.
const (
	AuditTargetType    = "supplier_fence"
	AuditActionAcquire = "supplier_fence.acquire"
	AuditActionCommit  = "supplier_fence.commit"
	AuditActionRelease = "supplier_fence.release"
)

// Service implements Fences on PostgreSQL.
type Service struct {
	repo   *opsrepo.Repository
	logger *slog.Logger
}

var _ Fences = (*Service)(nil)

func NewService(repo *opsrepo.Repository, logger *slog.Logger) *Service {
	if repo == nil {
		panic("fence: repository is required")
	}
	if logger == nil {
		panic("fence: logger is required")
	}
	return &Service{repo: repo, logger: logger}
}

// Acquire follows docs/contracts.md "Fence Acquire" step by step in one
// transaction: exclusive supplier lock, create the fence OPEN if absent, then
// count active errands. The count runs after the lock, so under READ
// COMMITTED it sees every creation and edit that committed while Acquire
// waited.
func (s *Service) Acquire(ctx context.Context, actor, operationID, supplierID string) (Fence, error) {
	var result Fence
	err := s.repo.InTx(ctx, func(tx *opsrepo.Repository) error {
		if err := tx.LockSupplier(ctx, supplierID); err != nil {
			return err
		}
		stored, _, err := tx.CreateFence(ctx, operationID, supplierID)
		if err != nil {
			return err
		}
		result, err = withActiveErrands(ctx, tx, stored)
		if err != nil {
			return err
		}
		return audit(ctx, tx, actor, AuditActionAcquire, operationID, opsrepo.AuditSucceeded, "")
	})
	if err != nil {
		return Fence{}, s.refuse(ctx, actor, AuditActionAcquire, operationID, err)
	}
	return result, nil
}

// Inspect reads the fence and counts active errands now. It takes no lock
// and changes nothing, so it is not audited.
func (s *Service) Inspect(ctx context.Context, operationID string) (Fence, error) {
	stored, err := s.repo.GetFence(ctx, operationID)
	if err != nil {
		return Fence{}, classify(err)
	}
	result, err := withActiveErrands(ctx, s.repo, stored)
	if err != nil {
		return Fence{}, classify(err)
	}
	return result, nil
}

// Commit makes the block permanent (OPEN to COMMITTED).
func (s *Service) Commit(ctx context.Context, actor, operationID string) (Fence, error) {
	return s.close(ctx, actor, AuditActionCommit, operationID, opsrepo.FenceCommitted)
}

// Release lifts the block (OPEN to RELEASED).
func (s *Service) Release(ctx context.Context, actor, operationID string) (Fence, error) {
	return s.close(ctx, actor, AuditActionRelease, operationID, opsrepo.FenceReleased)
}

// close needs no advisory lock: it changes only the fence row
// (docs/contracts.md, "Fence Acquire").
func (s *Service) close(ctx context.Context, actor, action, operationID string, state opsrepo.FenceState) (Fence, error) {
	var result Fence
	err := s.repo.InTx(ctx, func(tx *opsrepo.Repository) error {
		stored, err := tx.CloseFence(ctx, operationID, state)
		if err != nil {
			return err
		}
		result, err = withActiveErrands(ctx, tx, stored)
		if err != nil {
			return err
		}
		return audit(ctx, tx, actor, action, operationID, opsrepo.AuditSucceeded, "")
	})
	if err != nil {
		return Fence{}, s.refuse(ctx, actor, action, operationID, err)
	}
	return result, nil
}

// refuse classifies a failed mutation. A refusal (the transaction rolled
// back) is audited as REJECTED outside the transaction; an internal failure
// is returned as is. A failed rejection audit is logged and does not change
// the answer.
func (s *Service) refuse(ctx context.Context, actor, action, operationID string, err error) error {
	classified := classify(err)
	var appErr *apperror.Error
	if !errors.As(classified, &appErr) {
		return classified
	}
	if auditErr := audit(ctx, s.repo, actor, action, operationID, opsrepo.AuditRejected, string(appErr.Code)); auditErr != nil {
		s.logger.ErrorContext(ctx, "fence rejection audit failed",
			slog.String("action", action),
			slog.String("operation_id", operationID),
			slog.String("code", string(appErr.Code)),
			slog.Any("error", auditErr))
	}
	return classified
}

func withActiveErrands(ctx context.Context, repo *opsrepo.Repository, stored opsrepo.Fence) (Fence, error) {
	count, err := repo.CountActiveErrandsForSupplier(ctx, stored.SupplierID)
	if err != nil {
		return Fence{}, err
	}
	state, err := wireState(stored.State)
	if err != nil {
		return Fence{}, err
	}
	return Fence{
		OperationID:      stored.OperationID,
		SupplierID:       stored.SupplierID,
		State:            state,
		HasActiveErrands: count > 0,
	}, nil
}

func audit(ctx context.Context, repo *opsrepo.Repository, actor, action, operationID string, outcome opsrepo.AuditOutcome, reason string) error {
	_, err := repo.InsertAudit(ctx, opsrepo.AuditRecord{
		ActorID:    actor,
		Action:     action,
		TargetType: AuditTargetType,
		TargetID:   operationID,
		Reason:     reason,
		Outcome:    outcome,
	})
	return err
}

func wireState(state opsrepo.FenceState) (State, error) {
	switch state {
	case opsrepo.FenceOpen:
		return StateOpen, nil
	case opsrepo.FenceCommitted:
		return StateCommitted, nil
	case opsrepo.FenceReleased:
		return StateReleased, nil
	default:
		return "", fmt.Errorf("fence: unknown stored state %q", state)
	}
}

// classify maps repository sentinels to the codes in docs/contracts.md
// ("Wire format"). Anything else is left for the handler to report as
// INTERNAL.
func classify(err error) error {
	switch {
	case errors.Is(err, opsrepo.ErrFenceSupplierMismatch):
		return &apperror.Error{Code: apperror.InvalidArgument, Message: "operationId is already used for another supplier"}
	case errors.Is(err, opsrepo.ErrFenceStateConflict):
		return &apperror.Error{Code: apperror.InvalidTransition, Message: "fence is already in the other terminal state"}
	case errors.Is(err, opsrepo.ErrNotFound):
		return &apperror.Error{Code: apperror.FenceNotFound, Message: "fence not found"}
	case errors.Is(err, opsrepo.ErrInvalidArgument):
		return &apperror.Error{Code: apperror.InvalidArgument, Message: "operationId and supplierId must be UUIDs"}
	default:
		return err
	}
}
