package supplierrepo

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
)

func TestConcurrentDuplicateCreatesUseDatabaseUniqueness(t *testing.T) {
	repository, pool := migratedRepository(t)
	ctx := context.Background()
	start := make(chan struct{})
	results := make(chan error, 2)
	names := []string{"NUS   Co-Op", "  nus co-op  "}

	for _, name := range names {
		name := name
		go func() {
			<-start
			details := validDetails(name)
			_, err := repository.Create(ctx, details, time.Now().UTC())
			results <- err
		}()
	}
	close(start)

	assertOneSuccessOneNameConflict(t, <-results, <-results)
	var suppliers, versions int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM suppliers`).Scan(&suppliers))
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM supplier_versions`).Scan(&versions))
	require.Equal(t, 1, suppliers)
	require.Equal(t, 1, versions)
}

func TestConcurrentDuplicateRenamesRollbackLosingVersion(t *testing.T) {
	repository, pool := migratedRepository(t)
	ctx := context.Background()
	first, err := repository.Create(ctx, validDetails("First Supplier"), time.Now().UTC())
	require.NoError(t, err)
	second, err := repository.Create(ctx, validDetails("Second Supplier"), time.Now().UTC())
	require.NoError(t, err)

	start := make(chan struct{})
	results := make(chan error, 2)
	targetNames := []string{"Shared Name", "  shared   name "}
	ids := []supplier.SupplierID{first.SupplierID, second.SupplierID}
	for index := range ids {
		index := index
		go func() {
			<-start
			_, updateErr := repository.Update(ctx, ids[index], supplier.Patch{Name: &targetNames[index]}, time.Now().UTC())
			results <- updateErr
		}()
	}
	close(start)

	assertOneSuccessOneNameConflict(t, <-results, <-results)
	var versions, renamed int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM supplier_versions`).Scan(&versions))
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT count(*) FROM suppliers
		WHERE deleted_at IS NULL AND current_normalized_name = 'shared name'
	`).Scan(&renamed))
	require.Equal(t, 3, versions, "the conflicting append must roll back with its pointer update")
	require.Equal(t, 1, renamed)
}

func TestConcurrentUpdatesValidateAgainstLatestLockedVersion(t *testing.T) {
	repository, pool := migratedRepository(t)
	ctx := context.Background()
	created, err := repository.Create(ctx, validDetails("Concurrent Supplier"), time.Now().UTC())
	require.NoError(t, err)

	opening := "10:00"
	closing := "10:00"
	patches := []supplier.Patch{{OpeningTime: &opening}, {ClosingTime: &closing}}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, patch := range patches {
		patch := patch
		go func() {
			<-start
			_, updateErr := repository.Update(ctx, created.SupplierID, patch, time.Now().UTC())
			results <- updateErr
		}()
	}
	close(start)

	errorsSeen := []error{<-results, <-results}
	var successes, validationFailures int
	for _, result := range errorsSeen {
		if result == nil {
			successes++
			continue
		}
		var validationError *supplier.ValidationError
		if errors.As(result, &validationError) {
			validationFailures++
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, validationFailures)

	current, err := repository.GetCurrentAvailable(ctx, created.SupplierID)
	require.NoError(t, err)
	require.NotEqual(t, current.Details.OpeningTime, current.Details.ClosingTime)
	var versions int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT count(*) FROM supplier_versions WHERE supplier_id = $1
	`, created.SupplierID).Scan(&versions))
	require.Equal(t, 2, versions)
}

func TestStoredOrderVersionRemainsUnchangedAfterUpdateAndDeletion(t *testing.T) {
	repository, _ := migratedRepository(t)
	ctx := context.Background()
	createdAt := time.Now().UTC()
	originalDetails := validDetails("Order Pickup Supplier")
	created, err := repository.Create(ctx, originalDetails, createdAt)
	require.NoError(t, err)

	// An order stores this immutable reference when it is created.
	orderVersionID := created.VersionID
	newLocation := "Level 2, beside the lift"
	updated, err := repository.Update(ctx, created.SupplierID, supplier.Patch{
		LocationDescription: &newLocation,
	}, createdAt.Add(time.Minute))
	require.NoError(t, err)
	require.NotEqual(t, orderVersionID, updated.VersionID)

	storedPickupAfterUpdate, err := repository.GetVersion(ctx, orderVersionID)
	require.NoError(t, err)
	require.Equal(t, originalDetails, storedPickupAfterUpdate.Details)
	require.False(t, storedPickupAfterUpdate.Available)

	operationID := supplier.DeletionOperationID(testUUID(t))
	_, err = repository.CreateOrGetDeletion(ctx, operationID, created.SupplierID, 1, createdAt.Add(2*time.Minute))
	require.NoError(t, err)
	claimID := supplier.DeletionClaimID(testUUID(t))
	_, claimed, err := repository.ClaimDeletion(
		ctx,
		operationID,
		claimID,
		createdAt.Add(2*time.Minute),
		createdAt.Add(3*time.Minute),
	)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, repository.DeleteCurrentAndMarkCommitPending(
		ctx,
		created.SupplierID,
		operationID,
		claimID,
		createdAt.Add(2*time.Minute),
	))

	storedPickupAfterDeletion, err := repository.GetVersion(ctx, orderVersionID)
	require.NoError(t, err)
	require.Equal(t, storedPickupAfterUpdate, storedPickupAfterDeletion)
	require.Equal(t, originalDetails, storedPickupAfterDeletion.Details)

	deletedCurrentVersion, err := repository.GetVersion(ctx, updated.VersionID)
	require.NoError(t, err)
	require.Equal(t, updated.Details, deletedCurrentVersion.Details)
	require.False(t, deletedCurrentVersion.Available)

	_, err = repository.GetCurrentAvailable(ctx, created.SupplierID)
	requireApplicationCode(t, err, apperror.SupplierNotFound)
	page, err := repository.ListAvailable(ctx, supplier.ListFilter{})
	require.NoError(t, err)
	require.Empty(t, page.Items)
}

func TestReplayedDeletionOperationIsCreatedClaimedAndCompletedOnce(t *testing.T) {
	repository, pool := migratedRepository(t)
	ctx := context.Background()
	created, err := repository.Create(ctx, validDetails("Deletion Supplier"), time.Now().UTC())
	require.NoError(t, err)
	operationID := supplier.DeletionOperationID(testUUID(t))
	now := time.Now().UTC()

	const callers = 8
	start := make(chan struct{})
	records := make(chan supplier.DeletionRecord, callers)
	errorsSeen := make(chan error, callers)
	var workers sync.WaitGroup
	workers.Add(callers)
	for range callers {
		go func() {
			defer workers.Done()
			<-start
			record, createErr := repository.CreateOrGetDeletion(ctx, operationID, created.SupplierID, 5, now)
			records <- record
			errorsSeen <- createErr
		}()
	}
	close(start)
	workers.Wait()
	close(records)
	close(errorsSeen)
	for createErr := range errorsSeen {
		require.NoError(t, createErr)
	}
	for record := range records {
		require.Equal(t, operationID, record.OperationID)
		require.Equal(t, created.SupplierID, record.SupplierID)
		require.Equal(t, supplier.DeletionRequested, record.State)
		require.Equal(t, 5, record.MaxAttempts)
	}
	var operationCount int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT count(*) FROM supplier_deletion_operations WHERE operation_id = $1
	`, operationID).Scan(&operationCount))
	require.Equal(t, 1, operationCount)
	otherSupplier, err := repository.Create(ctx, validDetails("Other Supplier"), now)
	require.NoError(t, err)
	_, err = repository.CreateOrGetDeletion(ctx, operationID, otherSupplier.SupplierID, 5, now)
	require.ErrorIs(t, err, supplier.ErrDeletionOperationMismatch)

	claimIDs := []supplier.DeletionClaimID{
		supplier.DeletionClaimID(testUUID(t)),
		supplier.DeletionClaimID(testUUID(t)),
	}
	claimResults := make(chan bool, 2)
	claimErrors := make(chan error, 2)
	start = make(chan struct{})
	for _, claimID := range claimIDs {
		claimID := claimID
		go func() {
			<-start
			_, claimed, claimErr := repository.ClaimDeletion(ctx, operationID, claimID, now, now.Add(time.Minute))
			claimResults <- claimed
			claimErrors <- claimErr
		}()
	}
	close(start)
	claimedCount := 0
	for range 2 {
		require.NoError(t, <-claimErrors)
		if <-claimResults {
			claimedCount++
		}
	}
	// Channel receive order is unrelated to claimIDs, so read the durable winner.
	claimedRecord, err := repository.GetDeletion(ctx, operationID)
	require.NoError(t, err)
	winningClaim := claimedRecord.ClaimID
	require.Equal(t, 1, claimedCount)
	require.Equal(t, 1, claimedRecord.AttemptCount)

	require.NoError(t, repository.DeleteCurrentAndMarkCommitPending(
		ctx, created.SupplierID, operationID, winningClaim, now.Add(time.Second),
	))
	_, err = repository.GetCurrentAvailable(ctx, created.SupplierID)
	requireApplicationCode(t, err, apperror.SupplierNotFound)
	historical, err := repository.GetVersion(ctx, created.VersionID)
	require.NoError(t, err)
	require.False(t, historical.Available)

	require.NoError(t, repository.CompleteDeletion(ctx, operationID, winningClaim, now.Add(2*time.Second)))
	replayed, err := repository.CreateOrGetDeletion(ctx, operationID, created.SupplierID, 99, now.Add(3*time.Second))
	require.NoError(t, err)
	require.Equal(t, supplier.DeletionCompleted, replayed.State)
	require.Equal(t, 5, replayed.MaxAttempts, "replay must not reset operation metadata")
	require.Equal(t, 1, replayed.AttemptCount)
}

func TestDeletionRetryMetadataPendingBoundsAndTransitions(t *testing.T) {
	repository, _ := migratedRepository(t)
	ctx := context.Background()
	created, err := repository.Create(ctx, validDetails("Retry Supplier"), time.Now().UTC())
	require.NoError(t, err)
	operationID := supplier.DeletionOperationID(testUUID(t))
	now := time.Now().UTC()
	_, err = repository.CreateOrGetDeletion(ctx, operationID, created.SupplierID, 2, now)
	require.NoError(t, err)

	due, err := repository.ListClaimableDeletions(ctx, now, 1)
	require.NoError(t, err)
	require.Len(t, due, 1)
	_, err = repository.ListClaimableDeletions(ctx, now, 101)
	requireApplicationCode(t, err, apperror.InvalidArgument)

	claimID := supplier.DeletionClaimID(testUUID(t))
	_, claimed, err := repository.ClaimDeletion(ctx, operationID, claimID, now, now.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, repository.MarkDeletionReleasePending(ctx, operationID, claimID, now.Add(time.Second)))
	nextAttempt := now.Add(10 * time.Minute)
	require.NoError(t, repository.ScheduleDeletionRetry(
		ctx, operationID, claimID, supplier.DeletionFailureReleaseUnavailable, &nextAttempt, now.Add(2*time.Second),
	))

	retried, err := repository.GetDeletion(ctx, operationID)
	require.NoError(t, err)
	require.Equal(t, supplier.DeletionReleasePending, retried.State)
	require.Equal(t, supplier.DeletionFailureReleaseUnavailable, retried.FailureCode)
	require.Empty(t, retried.ClaimID)
	require.WithinDuration(t, nextAttempt, *retried.NextAttemptAt, time.Microsecond)
	due, err = repository.ListClaimableDeletions(ctx, nextAttempt.Add(-time.Second), 10)
	require.NoError(t, err)
	require.Empty(t, due)

	secondClaim := supplier.DeletionClaimID(testUUID(t))
	_, claimed, err = repository.ClaimDeletion(ctx, operationID, secondClaim, nextAttempt, nextAttempt.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, repository.MarkDeletionRejectedActiveErrands(ctx, operationID, secondClaim, nextAttempt.Add(time.Second)))
	terminal, err := repository.GetDeletion(ctx, operationID)
	require.NoError(t, err)
	require.Equal(t, supplier.DeletionRejectedActiveErrands, terminal.State)
	require.Equal(t, 2, terminal.AttemptCount)
	due, err = repository.ListClaimableDeletions(ctx, nextAttempt.Add(time.Hour), 10)
	require.NoError(t, err)
	require.Empty(t, due)

	expiredOperationID := supplier.DeletionOperationID(testUUID(t))
	_, err = repository.CreateOrGetDeletion(ctx, expiredOperationID, created.SupplierID, 3, now)
	require.NoError(t, err)
	staleClaim := supplier.DeletionClaimID(testUUID(t))
	_, claimed, err = repository.ClaimDeletion(ctx, expiredOperationID, staleClaim, now, now.Add(time.Second))
	require.NoError(t, err)
	require.True(t, claimed)
	replacementClaim := supplier.DeletionClaimID(testUUID(t))
	_, claimed, err = repository.ClaimDeletion(ctx, expiredOperationID, replacementClaim, now.Add(2*time.Second), now.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, claimed, "an expired lease must be replaceable")
	err = repository.MarkDeletionReleasePending(ctx, expiredOperationID, staleClaim, now.Add(3*time.Second))
	require.ErrorIs(t, err, supplier.ErrDeletionClaimLost)

	rollbackSupplier, err := repository.Create(ctx, validDetails("Rollback Supplier"), now)
	require.NoError(t, err)
	err = repository.DeleteCurrentAndMarkCommitPending(
		ctx, rollbackSupplier.SupplierID, expiredOperationID, replacementClaim, now.Add(4*time.Second),
	)
	require.ErrorIs(t, err, supplier.ErrInvalidDeletionTransition)
	_, err = repository.GetCurrentAvailable(ctx, rollbackSupplier.SupplierID)
	require.NoError(t, err, "soft deletion must roll back when commit-pending cannot be recorded")
}

func assertOneSuccessOneNameConflict(t *testing.T, first, second error) {
	t.Helper()
	successes, conflicts := 0, 0
	for _, err := range []error{first, second} {
		if err == nil {
			successes++
			continue
		}
		if errors.Is(err, apperror.ErrSupplierNameConflict) {
			conflicts++
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, conflicts)
}

func requireApplicationCode(t *testing.T, err error, code apperror.Code) {
	t.Helper()
	require.Error(t, err)
	var applicationError *apperror.Error
	require.ErrorAs(t, err, &applicationError)
	require.Equal(t, code, applicationError.Code)
}

func validDetails(name string) supplier.Details {
	return supplier.Details{
		Name:                name,
		Type:                "food",
		Building:            "COM 3",
		Floor:               "1",
		LocationDescription: "Atrium",
		Latitude:            1.294,
		Longitude:           103.773,
		OpeningTime:         "09:00",
		ClosingTime:         "18:00",
	}
}

func migratedRepository(t *testing.T) (*Repository, *pgxpool.Pool) {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set; skipping PostgreSQL repository integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	admin, err := pgx.Connect(ctx, databaseURL)
	require.NoError(t, err)
	schema := "supplier_repository_" + strings.ReplaceAll(testUUID(t), "-", "")
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	_, err = admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema)
	require.NoError(t, err)

	config, err := pgxpool.ParseConfig(databaseURL)
	require.NoError(t, err)
	config.MaxConns = 12
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	require.NoError(t, err)
	require.NoError(t, pool.Ping(ctx))
	t.Cleanup(func() {
		pool.Close()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		_, _ = admin.Exec(cleanupCtx, "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
		_ = admin.Close(cleanupCtx)
	})

	_, sourceFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	migrationPath := filepath.Join(filepath.Dir(sourceFile), "..", "..", "..", "migrations", "00001_create_versioned_suppliers.sql")
	migration, err := os.ReadFile(migrationPath)
	require.NoError(t, err)
	upSQL, err := migrationUp(string(migration))
	require.NoError(t, err)
	_, err = pool.Exec(ctx, upSQL)
	require.NoError(t, err)

	return New(pool), pool
}

func migrationUp(migration string) (string, error) {
	upMarker := "-- +goose Up"
	downMarker := "-- +goose Down"
	upAt := strings.Index(migration, upMarker)
	downAt := strings.Index(migration, downMarker)
	if upAt < 0 || downAt <= upAt {
		return "", errors.New("migration is missing ordered up/down markers")
	}
	return migration[upAt+len(upMarker) : downAt], nil
}

func testUUID(t *testing.T) string {
	t.Helper()
	value := make([]byte, 16)
	_, err := rand.Read(value)
	require.NoError(t, err)
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}
