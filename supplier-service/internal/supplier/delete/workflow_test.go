package deletefeature

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
)

func TestDeleteSupplierInspectedFenceForAnotherSupplierFailsClosed(t *testing.T) {
	store := newFakeStore()
	fence := &fakeOrderDeletionFence{
		acquireErr: errors.New("400 INVALID_ARGUMENT"),
		inspectFence: Fence{
			OperationID: testOperationID,
			SupplierID:  supplier.SupplierID("44444444-4444-4444-8444-444444444444"),
			State:       FenceOpen,
		},
	}
	rec := serveDelete(t, store, fence, string(testSupplierID), string(testOperationID), adminPrincipal(), nil)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Equal(t, 0, store.deleteCalls, "a fence for another supplier never authorises deletion")
	require.Equal(t, 0, fence.commitCalls)
	require.Equal(t, supplier.DeletionFailureAcquireAmbiguous, store.lastFailureCode)
}

func TestDeleteSupplierSupplierIDComparedCaseInsensitively(t *testing.T) {
	store := newFakeStore()
	fence := &fakeOrderDeletionFence{acquireFence: Fence{
		OperationID: testOperationID,
		SupplierID:  supplier.SupplierID("29E9AA8B-5651-4981-8828-3FBB1B21FCB1"),
		State:       FenceOpen,
	}}
	rec := serveDelete(t, store, fence, string(testSupplierID), string(testOperationID), adminPrincipal(), nil)
	require.Equal(t, http.StatusNoContent, rec.Code)
}

func TestDeleteSupplierReleasedFenceStopsWithoutDeleting(t *testing.T) {
	store := newFakeStore()
	fence := &fakeOrderDeletionFence{acquireFence: Fence{
		OperationID: testOperationID,
		SupplierID:  testSupplierID,
		State:       FenceReleased,
	}}
	rec := serveDelete(t, store, fence, string(testSupplierID), string(testOperationID), adminPrincipal(), nil)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	var errResp apperror.Error
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &errResp))
	require.Equal(t, apperror.DeletionFenceUnavailable, errResp.Code)
	require.Contains(t, errResp.Message, "new Idempotency-Key")

	require.Equal(t, 0, store.deleteCalls)
	require.Equal(t, 0, fence.commitCalls)
	require.Equal(t, 1, store.retryCalls)
	require.Nil(t, store.lastNextAttempt, "a released operation must not be retried")
}

func TestDeleteSupplierCommittedFenceFromEarlierAttemptCompletes(t *testing.T) {
	store := newFakeStore()
	fence := &fakeOrderDeletionFence{acquireFence: Fence{
		OperationID: testOperationID,
		SupplierID:  testSupplierID,
		State:       FenceCommitted,
	}}
	rec := serveDelete(t, store, fence, string(testSupplierID), string(testOperationID), adminPrincipal(), nil)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, 1, store.deleteCalls)
	require.Equal(t, 1, fence.commitCalls)
	require.Equal(t, supplier.DeletionCompleted, store.records[testOperationID].State)
}

func TestDeleteSupplierUnknownFenceStateFailsClosed(t *testing.T) {
	store := newFakeStore()
	fence := &fakeOrderDeletionFence{acquireFence: Fence{OperationID: testOperationID, SupplierID: testSupplierID}}
	rec := serveDelete(t, store, fence, string(testSupplierID), string(testOperationID), adminPrincipal(), nil)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Equal(t, 0, store.deleteCalls)
}

func TestDeleteSupplierLostClaimDoesNotReleaseFence(t *testing.T) {
	store := newFakeStore()
	store.stealClaimOnDelete = true
	store.deleteCurrentErr = supplier.ErrDeletionClaimLost
	fence := &fakeOrderDeletionFence{acquireFence: Fence{
		OperationID: testOperationID,
		SupplierID:  testSupplierID,
		State:       FenceOpen,
	}}
	rec := serveDelete(t, store, fence, string(testSupplierID), string(testOperationID), adminPrincipal(), nil)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Equal(t, 0, fence.releaseCalls, "the worker that now owns the operation may depend on the fence")
}

func TestDeleteSupplierCommitFailureNeverReleasesFence(t *testing.T) {
	store := newFakeStore()
	fence := &fakeOrderDeletionFence{
		acquireFence: Fence{OperationID: testOperationID, SupplierID: testSupplierID, State: FenceOpen},
		commitErr:    errors.New("timeout"),
		inspectFence: Fence{OperationID: testOperationID, SupplierID: testSupplierID, State: FenceOpen},
	}
	rec := serveDelete(t, store, fence, string(testSupplierID), string(testOperationID), adminPrincipal(), nil)
	require.Equal(t, http.StatusAccepted, rec.Code)
	require.Equal(t, 0, fence.releaseCalls)
	require.Equal(t, supplier.DeletionCommitPending, store.records[testOperationID].State)
	require.NotNil(t, store.lastNextAttempt)
}
