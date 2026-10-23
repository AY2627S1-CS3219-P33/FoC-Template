package deletefeature

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
)

const (
	defaultMaxAttempts   = 5
	defaultLeaseDuration = 60 * time.Second
	defaultRetryBackoff  = 30 * time.Second
)

// Handler serves guarded supplier deletion requests for administrators.
type Handler struct {
	store         supplier.DeletionStore
	fence         OrderDeletionFence
	maxAttempts   int
	leaseDuration time.Duration
	retryBackoff  time.Duration
	now           func() time.Time
	newClaimID    func() supplier.DeletionClaimID
}

// NewHandler constructs a Handler backed by the provided DeletionStore and OrderDeletionFence.
func NewHandler(store supplier.DeletionStore, fence OrderDeletionFence) Handler {
	if store == nil {
		panic("delete: store is required")
	}
	if fence == nil {
		panic("delete: fence is required")
	}
	return Handler{
		store:         store,
		fence:         fence,
		maxAttempts:   defaultMaxAttempts,
		leaseDuration: defaultLeaseDuration,
		retryBackoff:  defaultRetryBackoff,
		now:           func() time.Time { return time.Now().UTC() },
		newClaimID:    defaultClaimID,
	}
}

// NewHandlerWithClock constructs a Handler with a custom clock function for testing.
func NewHandlerWithClock(store supplier.DeletionStore, fence OrderDeletionFence, now func() time.Time) Handler {
	if store == nil {
		panic("delete: store is required")
	}
	if fence == nil {
		panic("delete: fence is required")
	}
	if now == nil {
		panic("delete: clock is required")
	}
	return Handler{
		store:         store,
		fence:         fence,
		maxAttempts:   defaultMaxAttempts,
		leaseDuration: defaultLeaseDuration,
		retryBackoff:  defaultRetryBackoff,
		now:           now,
		newClaimID:    defaultClaimID,
	}
}

// RegisterRoutes registers the delete endpoint on the HTTP mux.
func (h Handler) RegisterRoutes(router *http.ServeMux) {
	router.HandleFunc("DELETE /suppliers/{supplierId}", h.delete)
}

type deletionPendingResponse struct {
	OperationID string `json:"operationId"`
	State       string `json:"state"`
}

func (h Handler) delete(response http.ResponseWriter, request *http.Request) {
	principal, ok := auth.PrincipalFromContext(request.Context())
	if !ok {
		writeError(response, http.StatusUnauthorized, apperror.Unauthenticated, "authentication is required")
		return
	}
	if !principal.Has(auth.ManageSuppliers) {
		writeError(response, http.StatusForbidden, apperror.Forbidden, "insufficient permissions for this operation")
		return
	}

	supplierID := supplier.SupplierID(request.PathValue("supplierId"))
	if !supplier.ValidSupplierID(supplierID) {
		writeError(response, http.StatusBadRequest, apperror.InvalidArgument, "supplierId must be a UUID")
		return
	}

	idempotencyKey := strings.TrimSpace(request.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" {
		writeError(response, http.StatusBadRequest, apperror.InvalidArgument, "Idempotency-Key header is required")
		return
	}

	opID := supplier.DeletionOperationID(idempotencyKey)
	if !supplier.ValidDeletionOperationID(opID) {
		writeError(response, http.StatusBadRequest, apperror.InvalidArgument, "Idempotency-Key must be a UUID")
		return
	}

	now := h.now()
	ctx := request.Context()

	record, err := h.store.CreateOrGetDeletion(ctx, opID, supplierID, h.maxAttempts, now)
	if err != nil {
		if errors.Is(err, supplier.ErrDeletionOperationMismatch) {
			writeError(response, http.StatusBadRequest, apperror.InvalidArgument, "idempotency key already used for another supplier")
			return
		}
		if isSupplierNotFound(err) {
			writeError(response, http.StatusNotFound, apperror.SupplierNotFound, "supplier not found")
			return
		}
		writeError(response, http.StatusInternalServerError, apperror.Internal, "an unexpected internal error occurred")
		return
	}

	// 1. Terminal states: Replays return the terminal outcome immediately without acquiring a lease.
	switch record.State {
	case supplier.DeletionCompleted:
		response.WriteHeader(http.StatusNoContent)
		return
	case supplier.DeletionRejectedActiveErrands:
		writeError(response, http.StatusConflict, apperror.SupplierHasActiveErrands, "supplier has active errands")
		return
	}

	// 2. Non-terminal states: Worker must hold the claim lease before interacting with the fence or modifying state.
	claimID := h.newClaimID()
	leaseExpiresAt := now.Add(h.leaseDuration)
	claimedRecord, claimed, err := h.store.ClaimDeletion(ctx, opID, claimID, now, leaseExpiresAt)
	if err != nil {
		writeError(response, http.StatusInternalServerError, apperror.Internal, "an unexpected internal error occurred")
		return
	}

	if !claimed {
		// Operation cannot be claimed (lease held by another worker, not due yet, or max attempts exhausted).
		switch record.State {
		case supplier.DeletionCommitPending:
			writeDeletionPending(response, opID)
			return
		default:
			// Unresolved pre-delete safety checks fail closed without deleting.
			writeError(response, http.StatusServiceUnavailable, apperror.DeletionFenceUnavailable, "deletion fence is unavailable")
			return
		}
	}

	// 3. Advance the claimed operation and report where it stopped.
	switch h.workflow().advance(ctx, claimedRecord, claimID, now) {
	case OutcomeCompleted:
		response.WriteHeader(http.StatusNoContent)
	case OutcomeRejectedActiveErrands:
		writeError(response, http.StatusConflict, apperror.SupplierHasActiveErrands, "supplier has active errands")
	case OutcomeCommitPending:
		writeDeletionPending(response, opID)
	case OutcomeFenceUnavailable:
		writeError(response, http.StatusServiceUnavailable, apperror.DeletionFenceUnavailable, "deletion fence is unavailable")
	case OutcomeReleaseUnavailable:
		writeError(response, http.StatusServiceUnavailable, apperror.DeletionFenceUnavailable, "deletion fence could not be released safely")
	case OutcomeFenceReleased:
		writeError(response, http.StatusServiceUnavailable, apperror.DeletionFenceUnavailable, "deletion fence was released; retry with a new Idempotency-Key")
	case OutcomeSupplierNotFound:
		writeError(response, http.StatusNotFound, apperror.SupplierNotFound, "supplier not found")
	default:
		writeError(response, http.StatusInternalServerError, apperror.Internal, "an unexpected internal error occurred")
	}
}

func (h Handler) workflow() workflow {
	return workflow{store: h.store, fence: h.fence, retryBackoff: h.retryBackoff}
}

func isSupplierNotFound(err error) bool {
	var appErr *apperror.Error
	return errors.As(err, &appErr) && appErr.Code == apperror.SupplierNotFound
}

func writeError(response http.ResponseWriter, status int, code apperror.Code, message string) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(apperror.Error{Code: code, Message: message})
}

func writeDeletionPending(response http.ResponseWriter, opID supplier.DeletionOperationID) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(response).Encode(deletionPendingResponse{
		OperationID: string(opID),
		State:       string(supplier.DeletionCommitPending),
	})
}

func defaultClaimID() supplier.DeletionClaimID {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return supplier.DeletionClaimID(fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]))
}
