package deletefeature

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
)

func testReconciler(store supplier.DeletionStore, fence OrderDeletionFence) Reconciler {
	r := NewReconciler(store, fence, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.now = func() time.Time { return testTime }
	r.newClaimID = func() supplier.DeletionClaimID { return testClaimID }
	return r
}

func pendingRecord(state supplier.DeletionState) supplier.DeletionRecord {
	due := testTime.Add(-time.Minute)
	return supplier.DeletionRecord{
		OperationID:   testOperationID,
		SupplierID:    testSupplierID,
		State:         state,
		MaxAttempts:   defaultMaxAttempts,
		AttemptCount:  1,
		NextAttemptAt: &due,
	}
}

func TestReconcilerCompletesCommitPendingDeletion(t *testing.T) {
	store := newFakeStore()
	store.records[testOperationID] = pendingRecord(supplier.DeletionCommitPending)
	fence := &fakeOrderDeletionFence{}

	require.Equal(t, 1, testReconciler(store, fence).ReconcileOnce(context.Background()))
	require.Equal(t, 1, fence.commitCalls)
	require.Equal(t, 0, fence.releaseCalls)
	require.Equal(t, testClaimID, store.lastClaimID)
	require.Equal(t, supplier.DeletionCompleted, store.records[testOperationID].State)
}

func TestReconcilerKeepsCommitPendingWhileOrderServiceIsDown(t *testing.T) {
	store := newFakeStore()
	store.records[testOperationID] = pendingRecord(supplier.DeletionCommitPending)
	fence := &fakeOrderDeletionFence{commitErr: errors.New("down"), inspectErr: errors.New("down")}

	testReconciler(store, fence).ReconcileOnce(context.Background())
	require.Equal(t, supplier.DeletionCommitPending, store.records[testOperationID].State)
	require.Equal(t, supplier.DeletionFailureCommitUnavailable, store.lastFailureCode)
	require.Equal(t, 0, fence.releaseCalls, "a soft-deleted supplier's fence is never released")
}

func TestReconcilerFinishesRejectionAfterReleaseFailure(t *testing.T) {
	store := newFakeStore()
	store.records[testOperationID] = pendingRecord(supplier.DeletionReleasePending)
	fence := &fakeOrderDeletionFence{}

	testReconciler(store, fence).ReconcileOnce(context.Background())
	require.Equal(t, 1, fence.releaseCalls)
	require.Equal(t, supplier.DeletionRejectedActiveErrands, store.records[testOperationID].State)
}

func TestReconcilerResumesRequestedDeletionThroughFence(t *testing.T) {
	store := newFakeStore()
	store.records[testOperationID] = pendingRecord(supplier.DeletionRequested)
	fence := &fakeOrderDeletionFence{acquireFence: Fence{
		OperationID: testOperationID, SupplierID: testSupplierID, State: FenceOpen, HasActiveErrands: true,
	}}

	testReconciler(store, fence).ReconcileOnce(context.Background())
	require.Equal(t, 1, fence.acquireCalls)
	require.Equal(t, 0, store.deleteCalls)
	require.Equal(t, supplier.DeletionRejectedActiveErrands, store.records[testOperationID].State)
}

func TestReconcilerSkipsOperationsClaimedElsewhere(t *testing.T) {
	store := newFakeStore()
	store.records[testOperationID] = pendingRecord(supplier.DeletionCommitPending)
	claimed := false
	store.claimResult = &claimed
	fence := &fakeOrderDeletionFence{}

	require.Equal(t, 0, testReconciler(store, fence).ReconcileOnce(context.Background()))
	require.Equal(t, 0, fence.commitCalls)
}

func TestReconcilerSurvivesListFailure(t *testing.T) {
	store := newFakeStore()
	store.listErr = errors.New("database down")
	require.Equal(t, 0, testReconciler(store, &fakeOrderDeletionFence{}).ReconcileOnce(context.Background()))
}

func TestReconcilerRunStopsWhenContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		testReconciler(newFakeStore(), &fakeOrderDeletionFence{}).Run(ctx)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reconciler did not stop")
	}
}

func TestNewReconcilerPanicsOnInvalidArguments(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	require.Panics(t, func() { NewReconciler(nil, &fakeOrderDeletionFence{}, time.Second, logger) })
	require.Panics(t, func() { NewReconciler(newFakeStore(), nil, time.Second, logger) })
	require.Panics(t, func() { NewReconciler(newFakeStore(), &fakeOrderDeletionFence{}, 0, logger) })
	require.Panics(t, func() { NewReconciler(newFakeStore(), &fakeOrderDeletionFence{}, time.Second, nil) })
}
