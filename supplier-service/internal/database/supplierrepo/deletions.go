package supplierrepo

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/apperror"
	db "github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/database/generated"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
)

const maximumPendingDeletionPage = 100

func (r *Repository) CreateOrGetDeletion(ctx context.Context, operationID supplier.DeletionOperationID, supplierID supplier.SupplierID, maxAttempts int, now time.Time) (supplier.DeletionRecord, error) {
	boundedAttempts, err := boundedInt32(maxAttempts, "maxAttempts")
	if err != nil {
		return supplier.DeletionRecord{}, err
	}
	row, err := r.queries.CreateOrGetDeletionOperation(ctx, db.CreateOrGetDeletionOperationParams{
		OperationID: string(operationID),
		SupplierID:  string(supplierID),
		MaxAttempts: boundedAttempts,
		Now:         timestamp(now),
	})
	if err != nil {
		var postgresError *pgconn.PgError
		if errors.As(err, &postgresError) && postgresError.Code == "23503" {
			return supplier.DeletionRecord{}, supplierNotFound()
		}
		return supplier.DeletionRecord{}, err
	}
	if row.SupplierID != string(supplierID) {
		return supplier.DeletionRecord{}, supplier.ErrDeletionOperationMismatch
	}
	return deletionRecord(
		row.OperationID, row.SupplierID, row.State, row.AttemptCount, row.MaxAttempts,
		row.NextAttemptAt, row.LastFailureCode, row.ClaimID, row.LeaseExpiresAt,
		row.RequestedAt, row.UpdatedAt, row.DeletedAt, row.CompletedAt,
	), nil
}

func (r *Repository) GetDeletion(ctx context.Context, operationID supplier.DeletionOperationID) (supplier.DeletionRecord, error) {
	row, err := r.queries.GetDeletionOperation(ctx, string(operationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return supplier.DeletionRecord{}, deletionOperationNotFound()
	}
	if err != nil {
		return supplier.DeletionRecord{}, err
	}
	return deletionRecord(
		row.OperationID, row.SupplierID, row.State, row.AttemptCount, row.MaxAttempts,
		row.NextAttemptAt, row.LastFailureCode, row.ClaimID, row.LeaseExpiresAt,
		row.RequestedAt, row.UpdatedAt, row.DeletedAt, row.CompletedAt,
	), nil
}

func (r *Repository) ListClaimableDeletions(ctx context.Context, now time.Time, limit int) ([]supplier.DeletionRecord, error) {
	if limit <= 0 || limit > maximumPendingDeletionPage {
		return nil, invalidArgument("limit must be between 1 and 100")
	}
	rows, err := r.queries.ListClaimableDeletionOperations(ctx, db.ListClaimableDeletionOperationsParams{
		Now:         timestamp(now),
		ResultLimit: int32(limit),
	})
	if err != nil {
		return nil, err
	}
	records := make([]supplier.DeletionRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, deletionRecord(
			row.OperationID, row.SupplierID, row.State, row.AttemptCount, row.MaxAttempts,
			row.NextAttemptAt, row.LastFailureCode, row.ClaimID, row.LeaseExpiresAt,
			row.RequestedAt, row.UpdatedAt, row.DeletedAt, row.CompletedAt,
		))
	}
	return records, nil
}

func (r *Repository) ClaimDeletion(ctx context.Context, operationID supplier.DeletionOperationID, claimID supplier.DeletionClaimID, now, leaseExpiresAt time.Time) (supplier.DeletionRecord, bool, error) {
	if !leaseExpiresAt.After(now) {
		return supplier.DeletionRecord{}, false, invalidArgument("lease expiry must be after claim time")
	}
	row, err := r.queries.ClaimDeletionOperation(ctx, db.ClaimDeletionOperationParams{
		ClaimID:        string(claimID),
		LeaseExpiresAt: timestamp(leaseExpiresAt),
		Now:            timestamp(now),
		OperationID:    string(operationID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return supplier.DeletionRecord{}, false, nil
	}
	if err != nil {
		return supplier.DeletionRecord{}, false, err
	}
	return deletionRecord(
		row.OperationID, row.SupplierID, row.State, row.AttemptCount, row.MaxAttempts,
		row.NextAttemptAt, row.LastFailureCode, row.ClaimID, row.LeaseExpiresAt,
		row.RequestedAt, row.UpdatedAt, row.DeletedAt, row.CompletedAt,
	), true, nil
}

func (r *Repository) MarkDeletionReleasePending(ctx context.Context, operationID supplier.DeletionOperationID, claimID supplier.DeletionClaimID, now time.Time) error {
	rows, err := r.queries.MarkDeletionReleasePending(ctx, db.MarkDeletionReleasePendingParams{
		Now: timestamp(now), OperationID: string(operationID), ClaimID: string(claimID),
	})
	return r.transitionResult(ctx, rows, err, operationID, claimID, now, supplier.DeletionRequested)
}

func (r *Repository) MarkDeletionRejectedActiveErrands(ctx context.Context, operationID supplier.DeletionOperationID, claimID supplier.DeletionClaimID, now time.Time) error {
	rows, err := r.queries.MarkDeletionRejectedActiveErrands(ctx, db.MarkDeletionRejectedActiveErrandsParams{
		Now: timestamp(now), OperationID: string(operationID), ClaimID: string(claimID),
	})
	return r.transitionResult(ctx, rows, err, operationID, claimID, now, supplier.DeletionReleasePending)
}

func (r *Repository) DeleteCurrentAndMarkCommitPending(ctx context.Context, supplierID supplier.SupplierID, operationID supplier.DeletionOperationID, claimID supplier.DeletionClaimID, now time.Time) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	queries := r.queries.WithTx(tx)

	rows, err := queries.SoftDeleteSupplier(ctx, db.SoftDeleteSupplierParams{
		Now: timestamp(now), SupplierID: string(supplierID),
	})
	if err != nil {
		return err
	}
	if rows != 1 {
		return supplierNotFound()
	}
	rows, err = queries.MarkDeletionCommitPending(ctx, db.MarkDeletionCommitPendingParams{
		Now:         timestamp(now),
		OperationID: string(operationID),
		SupplierID:  string(supplierID),
		ClaimID:     string(claimID),
	})
	if err != nil {
		return err
	}
	if rows != 1 {
		return diagnoseTransition(ctx, queries, operationID, claimID, now, supplier.DeletionRequested)
	}
	return tx.Commit(ctx)
}

func (r *Repository) CompleteDeletion(ctx context.Context, operationID supplier.DeletionOperationID, claimID supplier.DeletionClaimID, now time.Time) error {
	rows, err := r.queries.CompleteDeletionOperation(ctx, db.CompleteDeletionOperationParams{
		Now: timestamp(now), OperationID: string(operationID), ClaimID: string(claimID),
	})
	return r.transitionResult(ctx, rows, err, operationID, claimID, now, supplier.DeletionCommitPending)
}

func (r *Repository) ScheduleDeletionRetry(ctx context.Context, operationID supplier.DeletionOperationID, claimID supplier.DeletionClaimID, failureCode supplier.DeletionFailureCode, nextAttemptAt *time.Time, now time.Time) error {
	if !validFailureCode(failureCode) {
		return invalidArgument("deletion failure code is invalid")
	}
	rows, err := r.queries.ScheduleDeletionOperationRetry(ctx, db.ScheduleDeletionOperationRetryParams{
		NextAttemptAt:   nullableTimestamp(nextAttemptAt),
		LastFailureCode: pgtype.Text{String: string(failureCode), Valid: true},
		Now:             timestamp(now),
		OperationID:     string(operationID),
		ClaimID:         string(claimID),
	})
	return r.transitionResult(ctx, rows, err, operationID, claimID, now, "")
}

func (r *Repository) transitionResult(ctx context.Context, rows int64, err error, operationID supplier.DeletionOperationID, claimID supplier.DeletionClaimID, now time.Time, expected supplier.DeletionState) error {
	if err != nil {
		return err
	}
	if rows == 1 {
		return nil
	}
	return diagnoseTransition(ctx, r.queries, operationID, claimID, now, expected)
}

func diagnoseTransition(ctx context.Context, queries *db.Queries, operationID supplier.DeletionOperationID, claimID supplier.DeletionClaimID, now time.Time, expected supplier.DeletionState) error {
	row, err := queries.GetDeletionOperation(ctx, string(operationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return supplier.ErrInvalidDeletionTransition
	}
	if err != nil {
		return err
	}
	if row.ClaimID != string(claimID) || !row.LeaseExpiresAt.Valid || !row.LeaseExpiresAt.Time.After(now) {
		return supplier.ErrDeletionClaimLost
	}
	if expected != "" && supplier.DeletionState(row.State) != expected {
		return supplier.ErrInvalidDeletionTransition
	}
	return supplier.ErrInvalidDeletionTransition
}

func deletionRecord(operationID, supplierID, state string, attemptCount, maxAttempts int32, nextAttemptAt pgtype.Timestamptz, failureCode, claimID string, leaseExpiresAt, requestedAt, updatedAt, deletedAt, completedAt pgtype.Timestamptz) supplier.DeletionRecord {
	return supplier.DeletionRecord{
		OperationID:    supplier.DeletionOperationID(operationID),
		SupplierID:     supplier.SupplierID(supplierID),
		State:          supplier.DeletionState(state),
		AttemptCount:   int(attemptCount),
		MaxAttempts:    int(maxAttempts),
		NextAttemptAt:  optionalTime(nextAttemptAt),
		FailureCode:    supplier.DeletionFailureCode(failureCode),
		ClaimID:        supplier.DeletionClaimID(claimID),
		LeaseExpiresAt: optionalTime(leaseExpiresAt),
		RequestedAt:    requestedAt.Time,
		UpdatedAt:      updatedAt.Time,
		DeletedAt:      optionalTime(deletedAt),
		CompletedAt:    optionalTime(completedAt),
	}
}

func optionalTime(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time
	return &result
}

func validFailureCode(code supplier.DeletionFailureCode) bool {
	switch code {
	case supplier.DeletionFailureAcquireUnavailable,
		supplier.DeletionFailureAcquireAmbiguous,
		supplier.DeletionFailureInspectUnavailable,
		supplier.DeletionFailureReleaseUnavailable,
		supplier.DeletionFailureCommitUnavailable:
		return true
	default:
		return false
	}
}

func deletionOperationNotFound() error {
	return &apperror.Error{Code: apperror.InvalidArgument, Message: "deletion operation not found"}
}
