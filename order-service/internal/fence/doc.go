// Package fence serves the supplier deletion fence that supplier-service
// calls before and after deleting a supplier (F2.4.2, NFR7.3, gate C2). It
// implements order-service's side of supplier-service's docs/contracts.md
// ("Order-service deletion fence") and the locking protocol in
// docs/contracts.md ("Fence and creation locking").
//
// Routes (all need a service token with orders:internal, through
// serviceauth.RequireService):
//
//   - POST /internal/supplier-fences/{operationId}/acquire {supplierId}
//   - GET  /internal/supplier-fences/{operationId}
//   - POST /internal/supplier-fences/{operationId}/commit
//   - POST /internal/supplier-fences/{operationId}/release
//
// Each returns the Fence body {operationId, supplierId, state,
// hasActiveErrands}, with state in lower case (open, committed, released).
//
// Acquire runs in one READ COMMITTED transaction: it takes the exclusive
// advisory lock on the supplier key, creates the fence OPEN if the operation
// has none, then counts the supplier's active errands (including active
// errands with a pending edit onto the supplier). Errand creation and
// supplier-changing edits take the shared lock on the same key and check
// for an open or committed fence, so each one either commits before the
// fence and is counted, or is rejected. A replay for the same pair returns
// the fence's current state and never reopens it; another supplier for the
// same operation is 400 INVALID_ARGUMENT.
//
// Commit (OPEN to COMMITTED) and Release (OPEN to RELEASED) are idempotent.
// Moving a fence out of the other terminal state is 409 INVALID_TRANSITION:
// a committed fence is never lifted. A missing fence is 404 FENCE_NOT_FOUND.
//
// Every Acquire, Commit and Release writes an admin_audit row whose actor is
// the calling service's sub: SUCCEEDED in the same transaction as the change,
// or REJECTED (reason = the error code) when the request is refused.
package fence
