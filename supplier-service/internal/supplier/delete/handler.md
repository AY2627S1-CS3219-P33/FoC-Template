# Administrator Supplier Deletion Workflow

This document specifies the deletion workflow and error-handling cases enforced by the administrator deletion handler in [`handler.go`](./handler.go), adhering to the supplier service contract, commit specification B5, requirements F2.4, F2.4.2, F2.4.3, F2.4.4, NFR7.3.2, and NFR7.4.

---

## Handled Cases

| Case | Trigger Condition | HTTP Status | Error Code (`apperror.Code`) | Response / Action |
| :--- | :--- | :---: | :---: | :--- |
| **1. Unauthenticated Request** | Request context lacks an authenticated `auth.Principal` | `401 Unauthorized` | `UNAUTHENTICATED` | `"authentication is required"` |
| **2. Insufficient Permissions** | Authenticated caller lacks `auth.ManageSuppliers` (e.g., non-admin user) | `403 Forbidden` | `FORBIDDEN` | `"insufficient permissions for this operation"` |
| **3. Malformed `supplierId`** | Path parameter `{supplierId}` is not a valid UUID | `400 Bad Request` | `INVALID_ARGUMENT` | `"supplierId must be a UUID"` |
| **4. Missing `Idempotency-Key`** | Request header `Idempotency-Key` is missing or blank | `400 Bad Request` | `INVALID_ARGUMENT` | `"Idempotency-Key header is required"` |
| **5. Malformed `Idempotency-Key`** | Request header `Idempotency-Key` is not a valid UUID | `400 Bad Request` | `INVALID_ARGUMENT` | `"Idempotency-Key must be a UUID"` |
| **6. Idempotency Key Mismatch** | Operation ID is already bound to a different `supplierId` (NFR7.3.2) | `400 Bad Request` | `INVALID_ARGUMENT` | `"idempotency key already used for another supplier"` |
| **7. Supplier Not Found on Create** | Target `supplierId` does not exist in the database | `404 Not Found` | `SUPPLIER_NOT_FOUND` | `"supplier not found"` |
| **8. Replay of Completed Operation** | Operation was already committed and completed | `204 No Content` | *None* | Empty response body |
| **9. Replay of Rejected Operation** | Operation was already rejected due to active errands | `409 Conflict` | `SUPPLIER_HAS_ACTIVE_ERRANDS` | `"supplier has active errands"` |
| **10. Unclaimable Pre-Delete State** | Operation is in `requested` or `release_pending` but active lease or max attempts exhausted | `503 Service Unavailable` | `DELETION_FENCE_UNAVAILABLE` | `"deletion fence is unavailable"` *(never assumes deletion is safe)* |
| **11. Active Errands Detected** | Order service fence reports `HasActiveErrands: true` (F2.4.2) | `409 Conflict` | `SUPPLIER_HAS_ACTIVE_ERRANDS` | Marks `release_pending`, calls `Release`, marks `rejected_active_errands`, responds `"supplier has active errands"` |
| **12. Release Failure on Active Errands** | Active errands found, but `Release` call to order service fails | `503 Service Unavailable` | `DELETION_FENCE_UNAVAILABLE` | Schedules retry with `release_unavailable`, responds `"deletion fence could not be released safely"` |
| **13. Fence Acquire & Inspect Failure** | `Acquire` fails and disambiguating `Inspect` also fails | `503 Service Unavailable` | `DELETION_FENCE_UNAVAILABLE` | Schedules retry with `acquire_unavailable`, responds `"deletion fence is unavailable"` without deleting locally |
| **14. Supplier Write Failure** | Supplier write fails during `DeleteCurrentAndMarkCommitPending` (e.g. concurrent deletion) | `404 Not Found` / `500 Internal` | `SUPPLIER_NOT_FOUND` / `INTERNAL` | Drives fence to `Release` fail-closed to avoid blocking new orders, returns error |
| **15. Unconfirmed Fence Commit** | Supplier soft-deleted, but `Commit` to order service fails or times out (NFR7.4) | `202 Accepted` | *None* | Schedules retry with `commit_unavailable`, returns `{"operationId": "...", "state": "commit_pending"}` |
| **16. Disambiguated Commit Success** | `Commit` fails but subsequent `Inspect` confirms `committed` | `204 No Content` | *None* | Completes local operation and returns `204 No Content` |
| **17. Repository / System Failure** | Database or internal error occurs during operation creation or claim | `500 Internal Server Error` | `INTERNAL` | `"an unexpected internal error occurred"` *(prevents secret/credential leakage)* |
| **18. Successful Guarded Deletion** | Fence acquired with no active errands, supplier soft-deleted, commit confirmed (F2.4, F2.4.4) | `204 No Content` | *None* | Marks `completed`, returns `204 No Content` |

---

## Architectural Details

1. **Defense-in-Depth Authorization**: Verifies [`auth.PrincipalFromContext`](../auth/context.go) and asserts [`auth.ManageSuppliers`](../auth/port.go) permission before any operation or database side effect.
2. **Fail-Closed Distributed Protocol**: Never infers permission to delete from dependency failure. If order-service is unreachable or ambiguous, pre-delete safety checks return `503 DELETION_FENCE_UNAVAILABLE`.
3. **Safe State Transitions**:
   - `requested -> release_pending -> rejected_active_errands`
   - `requested -> commit_pending -> completed`
4. **Asynchronous Recovery & Pending Acknowledgment (NFR7.4)**: If local soft-delete succeeds but fence commit confirmation fails, the endpoint returns `202 Accepted` with `commit_pending`. The background reconciler (B6) continues the operation using the same operation ID.
5. **Deterministic Replays (NFR7.3.2)**: Every replay of an existing operation ID deterministically returns the terminal state or pending state, without creating duplicate fences or contradictory states.
6. **Immutable Version Preservation (F2.4.3, F2.5.3)**: Deletion soft-deletes the supplier record (`deleted_at IS NOT NULL`) without deleting historical snapshot rows in `supplier_versions`.
