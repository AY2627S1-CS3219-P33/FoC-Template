package deletefeature

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
)

var (
	testTime        = time.Date(2026, time.October, 2, 2, 0, 0, 0, time.UTC)
	testSupplierID  = supplier.SupplierID("29e9aa8b-5651-4981-8828-3fbb1b21fcb1")
	testOperationID = supplier.DeletionOperationID("a1b2c3d4-e5f6-4a1b-8c2d-3e4f5a6b7c8d")
	testClaimID     = supplier.DeletionClaimID("f1e2d3c4-b5a6-4f1e-8d2c-3b4a5f6e7d8c")
)

type fakeDeletionStore struct {
	records               map[supplier.DeletionOperationID]supplier.DeletionRecord
	createOrGetErr        error
	claimErr              error
	claimResult           *bool
	markReleasePendingErr error
	markRejectedErr       error
	deleteCurrentErr      error
	completeErr           error
	retryErr              error

	createOrGetCalls int
	claimCalls       int
	deleteCalls      int
	completeCalls    int
	retryCalls       int
	releaseCalls     int
	rejectedCalls    int

	lastClaimID     supplier.DeletionClaimID
	lastFailureCode supplier.DeletionFailureCode
	lastNextAttempt *time.Time
}

func newFakeStore() *fakeDeletionStore {
	return &fakeDeletionStore{
		records: make(map[supplier.DeletionOperationID]supplier.DeletionRecord),
	}
}

func (f *fakeDeletionStore) CreateOrGetDeletion(_ context.Context, opID supplier.DeletionOperationID, supplierID supplier.SupplierID, maxAttempts int, now time.Time) (supplier.DeletionRecord, error) {
	f.createOrGetCalls++
	if f.createOrGetErr != nil {
		return supplier.DeletionRecord{}, f.createOrGetErr
	}
	if rec, exists := f.records[opID]; exists {
		if rec.SupplierID != supplierID {
			return supplier.DeletionRecord{}, supplier.ErrDeletionOperationMismatch
		}
		return rec, nil
	}
	rec := supplier.DeletionRecord{
		OperationID: opID,
		SupplierID:  supplierID,
		State:       supplier.DeletionRequested,
		MaxAttempts: maxAttempts,
		RequestedAt: now,
		UpdatedAt:   now,
	}
	f.records[opID] = rec
	return rec, nil
}

func (f *fakeDeletionStore) GetDeletion(_ context.Context, opID supplier.DeletionOperationID) (supplier.DeletionRecord, error) {
	if rec, ok := f.records[opID]; ok {
		return rec, nil
	}
	return supplier.DeletionRecord{}, &apperror.Error{Code: apperror.InvalidArgument, Message: "not found"}
}

func (f *fakeDeletionStore) ListClaimableDeletions(_ context.Context, _ time.Time, _ int) ([]supplier.DeletionRecord, error) {
	return nil, nil
}

func (f *fakeDeletionStore) ClaimDeletion(_ context.Context, opID supplier.DeletionOperationID, claimID supplier.DeletionClaimID, now, leaseExpiresAt time.Time) (supplier.DeletionRecord, bool, error) {
	f.claimCalls++
	f.lastClaimID = claimID
	if f.claimErr != nil {
		return supplier.DeletionRecord{}, false, f.claimErr
	}
	if f.claimResult != nil && !*f.claimResult {
		rec := f.records[opID]
		return rec, false, nil
	}
	rec := f.records[opID]
	rec.ClaimID = claimID
	rec.AttemptCount++
	rec.LeaseExpiresAt = &leaseExpiresAt
	rec.UpdatedAt = now
	f.records[opID] = rec
	return rec, true, nil
}

func (f *fakeDeletionStore) MarkDeletionReleasePending(_ context.Context, opID supplier.DeletionOperationID, _ supplier.DeletionClaimID, now time.Time) error {
	f.releaseCalls++
	if f.markReleasePendingErr != nil {
		return f.markReleasePendingErr
	}
	rec := f.records[opID]
	rec.State = supplier.DeletionReleasePending
	rec.UpdatedAt = now
	f.records[opID] = rec
	return nil
}

func (f *fakeDeletionStore) MarkDeletionRejectedActiveErrands(_ context.Context, opID supplier.DeletionOperationID, _ supplier.DeletionClaimID, now time.Time) error {
	f.rejectedCalls++
	if f.markRejectedErr != nil {
		return f.markRejectedErr
	}
	rec := f.records[opID]
	rec.State = supplier.DeletionRejectedActiveErrands
	rec.UpdatedAt = now
	rec.ClaimID = ""
	rec.LeaseExpiresAt = nil
	f.records[opID] = rec
	return nil
}

func (f *fakeDeletionStore) DeleteCurrentAndMarkCommitPending(_ context.Context, _ supplier.SupplierID, opID supplier.DeletionOperationID, _ supplier.DeletionClaimID, now time.Time) error {
	f.deleteCalls++
	if f.deleteCurrentErr != nil {
		return f.deleteCurrentErr
	}
	rec := f.records[opID]
	rec.State = supplier.DeletionCommitPending
	rec.DeletedAt = &now
	rec.UpdatedAt = now
	f.records[opID] = rec
	return nil
}

func (f *fakeDeletionStore) CompleteDeletion(_ context.Context, opID supplier.DeletionOperationID, _ supplier.DeletionClaimID, now time.Time) error {
	f.completeCalls++
	if f.completeErr != nil {
		return f.completeErr
	}
	rec := f.records[opID]
	rec.State = supplier.DeletionCompleted
	rec.CompletedAt = &now
	rec.UpdatedAt = now
	rec.ClaimID = ""
	rec.LeaseExpiresAt = nil
	f.records[opID] = rec
	return nil
}

func (f *fakeDeletionStore) ScheduleDeletionRetry(_ context.Context, opID supplier.DeletionOperationID, _ supplier.DeletionClaimID, failureCode supplier.DeletionFailureCode, nextAttemptAt *time.Time, now time.Time) error {
	f.retryCalls++
	f.lastFailureCode = failureCode
	f.lastNextAttempt = nextAttemptAt
	if f.retryErr != nil {
		return f.retryErr
	}
	rec := f.records[opID]
	rec.FailureCode = failureCode
	rec.NextAttemptAt = nextAttemptAt
	rec.ClaimID = ""
	rec.LeaseExpiresAt = nil
	rec.UpdatedAt = now
	f.records[opID] = rec
	return nil
}

type fakeOrderDeletionFence struct {
	acquireFence Fence
	acquireErr   error
	inspectFence Fence
	inspectErr   error
	commitErr    error
	releaseErr   error

	acquireCalls int
	inspectCalls int
	commitCalls  int
	releaseCalls int

	lastOpID       supplier.DeletionOperationID
	lastSupplierID supplier.SupplierID
}

func (f *fakeOrderDeletionFence) Acquire(_ context.Context, opID supplier.DeletionOperationID, supplierID supplier.SupplierID) (Fence, error) {
	f.acquireCalls++
	f.lastOpID = opID
	f.lastSupplierID = supplierID
	if f.acquireErr != nil {
		return Fence{}, f.acquireErr
	}
	return f.acquireFence, nil
}

func (f *fakeOrderDeletionFence) Inspect(_ context.Context, opID supplier.DeletionOperationID) (Fence, error) {
	f.inspectCalls++
	f.lastOpID = opID
	if f.inspectErr != nil {
		return Fence{}, f.inspectErr
	}
	return f.inspectFence, nil
}

func (f *fakeOrderDeletionFence) Commit(_ context.Context, opID supplier.DeletionOperationID) error {
	f.commitCalls++
	f.lastOpID = opID
	return f.commitErr
}

func (f *fakeOrderDeletionFence) Release(_ context.Context, opID supplier.DeletionOperationID) error {
	f.releaseCalls++
	f.lastOpID = opID
	return f.releaseErr
}

func adminPrincipal() *auth.Principal {
	return &auth.Principal{
		Subject: "admin-subject",
		Roles:   []auth.Role{auth.RoleAdministrator},
	}
}

func userPrincipal() *auth.Principal {
	return &auth.Principal{
		Subject: "user-subject",
		Roles:   []auth.Role{auth.RoleUser},
	}
}

func serveDelete(t *testing.T, store supplier.DeletionStore, fence OrderDeletionFence, supplierID string, idempotencyKey string, principal *auth.Principal, clock func() time.Time) *httptest.ResponseRecorder {
	t.Helper()
	router := http.NewServeMux()
	if clock == nil {
		clock = func() time.Time { return testTime }
	}
	NewHandlerWithClock(store, fence, clock).RegisterRoutes(router)

	path := "/suppliers/" + supplierID
	req := httptest.NewRequest(http.MethodDelete, path, nil)
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	if principal != nil {
		req = req.WithContext(auth.ContextWithPrincipal(req.Context(), *principal))
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestDeleteSupplierUnauthenticated(t *testing.T) {
	store := newFakeStore()
	fence := &fakeOrderDeletionFence{}
	rec := serveDelete(t, store, fence, string(testSupplierID), string(testOperationID), nil, nil)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	var errResp apperror.Error
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &errResp))
	require.Equal(t, apperror.Unauthenticated, errResp.Code)
}

func TestDeleteSupplierForbiddenForNonAdmin(t *testing.T) {
	store := newFakeStore()
	fence := &fakeOrderDeletionFence{}
	rec := serveDelete(t, store, fence, string(testSupplierID), string(testOperationID), userPrincipal(), nil)
	require.Equal(t, http.StatusForbidden, rec.Code)
	var errResp apperror.Error
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &errResp))
	require.Equal(t, apperror.Forbidden, errResp.Code)
}

func TestDeleteSupplierRejectsInvalidSupplierIDInPath(t *testing.T) {
	store := newFakeStore()
	fence := &fakeOrderDeletionFence{}
	rec := serveDelete(t, store, fence, "not-a-valid-uuid", string(testOperationID), adminPrincipal(), nil)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	var errResp apperror.Error
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &errResp))
	require.Equal(t, apperror.InvalidArgument, errResp.Code)
	require.Contains(t, errResp.Message, "supplierId must be a UUID")
}

func TestDeleteSupplierRejectsMissingIdempotencyKey(t *testing.T) {
	store := newFakeStore()
	fence := &fakeOrderDeletionFence{}
	rec := serveDelete(t, store, fence, string(testSupplierID), "", adminPrincipal(), nil)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	var errResp apperror.Error
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &errResp))
	require.Equal(t, apperror.InvalidArgument, errResp.Code)
	require.Contains(t, errResp.Message, "Idempotency-Key header is required")
}

func TestDeleteSupplierRejectsInvalidIdempotencyKey(t *testing.T) {
	store := newFakeStore()
	fence := &fakeOrderDeletionFence{}
	rec := serveDelete(t, store, fence, string(testSupplierID), "not-a-uuid", adminPrincipal(), nil)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	var errResp apperror.Error
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &errResp))
	require.Equal(t, apperror.InvalidArgument, errResp.Code)
	require.Contains(t, errResp.Message, "Idempotency-Key must be a UUID")
}

func TestDeleteSupplierIdempotencyKeyMismatch(t *testing.T) {
	store := newFakeStore()
	store.records[testOperationID] = supplier.DeletionRecord{
		OperationID: testOperationID,
		SupplierID:  supplier.SupplierID("44444444-4444-4444-8444-444444444444"),
		State:       supplier.DeletionRequested,
	}
	fence := &fakeOrderDeletionFence{}
	rec := serveDelete(t, store, fence, string(testSupplierID), string(testOperationID), adminPrincipal(), nil)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	var errResp apperror.Error
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &errResp))
	require.Equal(t, apperror.InvalidArgument, errResp.Code)
	require.Contains(t, errResp.Message, "idempotency key already used for another supplier")
}

func TestDeleteSupplierNotFound(t *testing.T) {
	store := newFakeStore()
	store.createOrGetErr = &apperror.Error{Code: apperror.SupplierNotFound, Message: "supplier not found"}
	fence := &fakeOrderDeletionFence{}
	rec := serveDelete(t, store, fence, string(testSupplierID), string(testOperationID), adminPrincipal(), nil)
	require.Equal(t, http.StatusNotFound, rec.Code)
	var errResp apperror.Error
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &errResp))
	require.Equal(t, apperror.SupplierNotFound, errResp.Code)
}

func TestDeleteSupplierStoreInternalErrorOnCreate(t *testing.T) {
	store := newFakeStore()
	store.createOrGetErr = errors.New("db connection failure")
	fence := &fakeOrderDeletionFence{}
	rec := serveDelete(t, store, fence, string(testSupplierID), string(testOperationID), adminPrincipal(), nil)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	var errResp apperror.Error
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &errResp))
	require.Equal(t, apperror.Internal, errResp.Code)
}

func TestDeleteSupplierSuccessfulGuardedDeletion(t *testing.T) {
	store := newFakeStore()
	fence := &fakeOrderDeletionFence{
		acquireFence: Fence{
			OperationID:      testOperationID,
			State:            FenceOpen,
			HasActiveErrands: false,
		},
	}
	rec := serveDelete(t, store, fence, string(testSupplierID), string(testOperationID), adminPrincipal(), nil)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Empty(t, rec.Body.Bytes())

	require.Equal(t, 1, store.createOrGetCalls)
	require.Equal(t, 1, store.claimCalls)
	require.Equal(t, 1, fence.acquireCalls)
	require.Equal(t, 1, store.deleteCalls)
	require.Equal(t, 1, fence.commitCalls)
	require.Equal(t, 1, store.completeCalls)

	finalRec := store.records[testOperationID]
	require.Equal(t, supplier.DeletionCompleted, finalRec.State)
	require.NotNil(t, finalRec.CompletedAt)
}

func TestDeleteSupplierActiveErrandsRejection(t *testing.T) {
	store := newFakeStore()
	fence := &fakeOrderDeletionFence{
		acquireFence: Fence{
			OperationID:      testOperationID,
			State:            FenceOpen,
			HasActiveErrands: true,
		},
	}
	rec := serveDelete(t, store, fence, string(testSupplierID), string(testOperationID), adminPrincipal(), nil)
	require.Equal(t, http.StatusConflict, rec.Code)
	var errResp apperror.Error
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &errResp))
	require.Equal(t, apperror.SupplierHasActiveErrands, errResp.Code)

	require.Equal(t, 1, store.releaseCalls)
	require.Equal(t, 1, fence.releaseCalls)
	require.Equal(t, 1, store.rejectedCalls)
	require.Equal(t, 0, store.deleteCalls)

	finalRec := store.records[testOperationID]
	require.Equal(t, supplier.DeletionRejectedActiveErrands, finalRec.State)
}

func TestDeleteSupplierActiveErrandsReleaseFailure(t *testing.T) {
	store := newFakeStore()
	fence := &fakeOrderDeletionFence{
		acquireFence: Fence{
			OperationID:      testOperationID,
			State:            FenceOpen,
			HasActiveErrands: true,
		},
		releaseErr: errors.New("network error on release"),
	}
	rec := serveDelete(t, store, fence, string(testSupplierID), string(testOperationID), adminPrincipal(), nil)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	var errResp apperror.Error
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &errResp))
	require.Equal(t, apperror.DeletionFenceUnavailable, errResp.Code)

	require.Equal(t, 1, store.retryCalls)
	require.Equal(t, supplier.DeletionFailureReleaseUnavailable, store.lastFailureCode)
}

func TestDeleteSupplierAcquireFailureDisambiguatedByInspectNoActiveErrands(t *testing.T) {
	store := newFakeStore()
	fence := &fakeOrderDeletionFence{
		acquireErr: errors.New("timeout calling acquire"),
		inspectFence: Fence{
			OperationID:      testOperationID,
			State:            FenceOpen,
			HasActiveErrands: false,
		},
	}
	rec := serveDelete(t, store, fence, string(testSupplierID), string(testOperationID), adminPrincipal(), nil)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, 1, fence.acquireCalls)
	require.Equal(t, 1, fence.inspectCalls)
	require.Equal(t, 1, store.deleteCalls)
	require.Equal(t, 1, fence.commitCalls)
	require.Equal(t, 1, store.completeCalls)
}

func TestDeleteSupplierAcquireFailureDisambiguatedByInspectHasActiveErrands(t *testing.T) {
	store := newFakeStore()
	fence := &fakeOrderDeletionFence{
		acquireErr: errors.New("timeout calling acquire"),
		inspectFence: Fence{
			OperationID:      testOperationID,
			State:            FenceOpen,
			HasActiveErrands: true,
		},
	}
	rec := serveDelete(t, store, fence, string(testSupplierID), string(testOperationID), adminPrincipal(), nil)
	require.Equal(t, http.StatusConflict, rec.Code)
	var errResp apperror.Error
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &errResp))
	require.Equal(t, apperror.SupplierHasActiveErrands, errResp.Code)
	require.Equal(t, 1, fence.releaseCalls)
}

func TestDeleteSupplierAcquireAndInspectBothFail(t *testing.T) {
	store := newFakeStore()
	fence := &fakeOrderDeletionFence{
		acquireErr: errors.New("acquire failed"),
		inspectErr: errors.New("inspect failed"),
	}
	rec := serveDelete(t, store, fence, string(testSupplierID), string(testOperationID), adminPrincipal(), nil)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	var errResp apperror.Error
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &errResp))
	require.Equal(t, apperror.DeletionFenceUnavailable, errResp.Code)

	require.Equal(t, 1, store.retryCalls)
	require.Equal(t, supplier.DeletionFailureAcquireUnavailable, store.lastFailureCode)
	require.Equal(t, 0, store.deleteCalls)
}

func TestDeleteSupplierSoftDeleteFailsReleasesFence(t *testing.T) {
	store := newFakeStore()
	store.deleteCurrentErr = &apperror.Error{Code: apperror.SupplierNotFound, Message: "supplier not found"}
	fence := &fakeOrderDeletionFence{
		acquireFence: Fence{
			OperationID:      testOperationID,
			State:            FenceOpen,
			HasActiveErrands: false,
		},
	}
	rec := serveDelete(t, store, fence, string(testSupplierID), string(testOperationID), adminPrincipal(), nil)
	require.Equal(t, http.StatusNotFound, rec.Code)
	var errResp apperror.Error
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &errResp))
	require.Equal(t, apperror.SupplierNotFound, errResp.Code)
	require.Equal(t, 1, fence.releaseCalls, "fence must be released if supplier write fails")
}

func TestDeleteSupplierCommitFailsReturnsAccepted202(t *testing.T) {
	store := newFakeStore()
	fence := &fakeOrderDeletionFence{
		acquireFence: Fence{
			OperationID:      testOperationID,
			State:            FenceOpen,
			HasActiveErrands: false,
		},
		commitErr:  errors.New("timeout committing fence"),
		inspectErr: errors.New("inspect failed"),
	}
	rec := serveDelete(t, store, fence, string(testSupplierID), string(testOperationID), adminPrincipal(), nil)
	require.Equal(t, http.StatusAccepted, rec.Code)
	var pendingResp deletionPendingResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &pendingResp))
	require.Equal(t, string(testOperationID), pendingResp.OperationID)
	require.Equal(t, "commit_pending", pendingResp.State)

	require.Equal(t, 1, store.retryCalls)
	require.Equal(t, supplier.DeletionFailureCommitUnavailable, store.lastFailureCode)
}

func TestDeleteSupplierCommitFailsDisambiguatedByInspectCommitted(t *testing.T) {
	store := newFakeStore()
	fence := &fakeOrderDeletionFence{
		acquireFence: Fence{
			OperationID:      testOperationID,
			State:            FenceOpen,
			HasActiveErrands: false,
		},
		commitErr: errors.New("connection reset"),
		inspectFence: Fence{
			OperationID:      testOperationID,
			State:            FenceCommitted,
			HasActiveErrands: false,
		},
	}
	rec := serveDelete(t, store, fence, string(testSupplierID), string(testOperationID), adminPrincipal(), nil)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, 1, store.completeCalls)
}

func TestDeleteSupplierReplayCompleted(t *testing.T) {
	store := newFakeStore()
	store.records[testOperationID] = supplier.DeletionRecord{
		OperationID: testOperationID,
		SupplierID:  testSupplierID,
		State:       supplier.DeletionCompleted,
	}
	fence := &fakeOrderDeletionFence{}
	rec := serveDelete(t, store, fence, string(testSupplierID), string(testOperationID), adminPrincipal(), nil)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Empty(t, rec.Body.Bytes())
	require.Equal(t, 0, store.claimCalls)
}

func TestDeleteSupplierReplayRejectedActiveErrands(t *testing.T) {
	store := newFakeStore()
	store.records[testOperationID] = supplier.DeletionRecord{
		OperationID: testOperationID,
		SupplierID:  testSupplierID,
		State:       supplier.DeletionRejectedActiveErrands,
	}
	fence := &fakeOrderDeletionFence{}
	rec := serveDelete(t, store, fence, string(testSupplierID), string(testOperationID), adminPrincipal(), nil)
	require.Equal(t, http.StatusConflict, rec.Code)
	var errResp apperror.Error
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &errResp))
	require.Equal(t, apperror.SupplierHasActiveErrands, errResp.Code)
	require.Equal(t, 0, store.claimCalls)
}

func TestDeleteSupplierReplayCommitPendingUnclaimed(t *testing.T) {
	store := newFakeStore()
	store.records[testOperationID] = supplier.DeletionRecord{
		OperationID: testOperationID,
		SupplierID:  testSupplierID,
		State:       supplier.DeletionCommitPending,
	}
	claimFalse := false
	store.claimResult = &claimFalse
	fence := &fakeOrderDeletionFence{}
	rec := serveDelete(t, store, fence, string(testSupplierID), string(testOperationID), adminPrincipal(), nil)
	require.Equal(t, http.StatusAccepted, rec.Code)
	var pendingResp deletionPendingResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &pendingResp))
	require.Equal(t, string(testOperationID), pendingResp.OperationID)
	require.Equal(t, "commit_pending", pendingResp.State)
}

func TestDeleteSupplierReplayCommitPendingClaimedAndCommitted(t *testing.T) {
	store := newFakeStore()
	store.records[testOperationID] = supplier.DeletionRecord{
		OperationID: testOperationID,
		SupplierID:  testSupplierID,
		State:       supplier.DeletionCommitPending,
	}
	fence := &fakeOrderDeletionFence{}
	rec := serveDelete(t, store, fence, string(testSupplierID), string(testOperationID), adminPrincipal(), nil)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, 1, fence.commitCalls)
	require.Equal(t, 1, store.completeCalls)
}

func TestDeleteSupplierReplayRequestedUnclaimableFailsClosed(t *testing.T) {
	store := newFakeStore()
	store.records[testOperationID] = supplier.DeletionRecord{
		OperationID: testOperationID,
		SupplierID:  testSupplierID,
		State:       supplier.DeletionRequested,
	}
	claimFalse := false
	store.claimResult = &claimFalse
	fence := &fakeOrderDeletionFence{}
	rec := serveDelete(t, store, fence, string(testSupplierID), string(testOperationID), adminPrincipal(), nil)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	var errResp apperror.Error
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &errResp))
	require.Equal(t, apperror.DeletionFenceUnavailable, errResp.Code)
}

func TestDeleteSupplierReplayReleasePendingClaimedReleasesSuccessfully(t *testing.T) {
	store := newFakeStore()
	store.records[testOperationID] = supplier.DeletionRecord{
		OperationID: testOperationID,
		SupplierID:  testSupplierID,
		State:       supplier.DeletionReleasePending,
	}
	fence := &fakeOrderDeletionFence{}
	rec := serveDelete(t, store, fence, string(testSupplierID), string(testOperationID), adminPrincipal(), nil)
	require.Equal(t, http.StatusConflict, rec.Code)
	var errResp apperror.Error
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &errResp))
	require.Equal(t, apperror.SupplierHasActiveErrands, errResp.Code)
	require.Equal(t, 1, fence.releaseCalls)
	require.Equal(t, 1, store.rejectedCalls)
}

func TestNewHandlerPanicsOnNilArguments(t *testing.T) {
	store := newFakeStore()
	fence := &fakeOrderDeletionFence{}

	require.PanicsWithValue(t, "delete: store is required", func() {
		NewHandler(nil, fence)
	})
	require.PanicsWithValue(t, "delete: fence is required", func() {
		NewHandler(store, nil)
	})
	require.PanicsWithValue(t, "delete: clock is required", func() {
		NewHandlerWithClock(store, fence, nil)
	})
}
