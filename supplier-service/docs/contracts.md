# Supplier domain and API contracts

This document records the decisions shared by supplier, order, user, and API
code. The machine-readable HTTP contract is [openapi.yaml](../openapi.yaml).

## Identity and lifecycle

- `supplierId` is a service-generated UUID that never changes and is never
  accepted in create or patch payloads.
- `versionId` is a service-generated UUID for one immutable snapshot. Every
  create and successful patch produces a new `versionId`; existing versions
  are never edited.
- A non-deleted supplier has exactly one current version. Only that version is
  available for new errands. A superseded version has `available: false`.
- Delete is a soft delete of the supplier identity. It removes the supplier
  from current reads and makes every version unavailable for new errands. It
  does not remove immutable versions.
- `GET /suppliers` and `GET /suppliers/{supplierId}` return only current,
  available, non-deleted records. A deleted ID returns `SUPPLIER_NOT_FOUND`.
- `GET /supplier-versions/{versionId}` returns the immutable snapshot even
  after update or deletion. This is the read used to display active and past
  errand pickup details.
- Normalized names collapse whitespace and use lower case. They are unique
  among non-deleted suppliers; a deleted supplier's name may be reused because
  historical versions remain unambiguous by ID.

All catalogue responses include both `supplierId` and `versionId`. An order
stores both, with `versionId` being the authoritative pickup-details reference.

## Validation and list behavior

Create requires `name`, `type`, `building`, `floor`, `locationDescription`,
`latitude`, `longitude`, `openingTime`, and `closingTime`. `imageUrl` is
optional. Patch accepts those fields but rejects an empty object; setting
`imageUrl` to `null` removes it. IDs are never patchable.

Coordinates use latitude `[-90, 90]` and longitude `[-180, 180]`. Times use
24-hour `HH:MM` and must differ. Image URLs must be absolute HTTP(S) URLs. The
length bounds in the common OpenAPI fragment are the shared bounds used by Go
validation. Create inputs preserve coordinate presence separately from their
numeric value so an omitted coordinate cannot be mistaken for valid zero.

`GET /suppliers` accepts:

- `q`: normalized, case-insensitive match against name, building, floor, or
  location description;
- `type`: exact, case-insensitive type match;
- `limit`: 1-100, default 25;
- `cursor`: an opaque continuation value returned as `nextCursor`.

Results are ordered by normalized name and then `supplierId`, both ascending,
before applying the cursor. Clients must not parse or construct cursors.
`nextCursor` is omitted on the last page.

## Authorization and errors

All supplier and version endpoints require a verified Auth0 access token for
`https://api.foc.local/supplier-service` and explicit `suppliers:read`. Create,
patch, and delete additionally require `suppliers:manage`. Auth0 administrator
assignments must grant both permissions. `/readyz` is unauthenticated.
Supplier use cases consume the trusted `auth.Principal`: Subject is Auth0
`sub`, Permissions comes from the validated token, and Roles grants no access.
The `auth.Port` adapter performs local RS256/issuer/audience/time validation
using cached Auth0 public keys; see [Auth0 setup](auth0.md), including the
account-lifecycle freshness gate. Middleware stores verified principals with
`auth.WithPrincipal`; handlers retrieve them with `auth.PrincipalFromContext`.
These helpers are transport-neutral, and payload identity and role values are
ignored.

The authentication port returns stable failure kinds. Missing, invalid, or
expired credentials are `invalid_credential` and map to `401`. An
authoritatively disabled account is `account_disabled` and maps to `403`. An
identity-provider outage is `verifier_unavailable` and maps to `503`. Unknown
authentication failures map to `500`; credential contents and provider error
details must not cross this boundary.

Stable error codes are defined in `internal/apperror` and the common OpenAPI
fragment. Field validation uses `INVALID_ARGUMENT` plus a `fields` array.
Not-found current and historical reads intentionally have distinct codes.

| HTTP status | Stable code |
| --- | --- |
| 400 | `INVALID_ARGUMENT` |
| 401 | `UNAUTHENTICATED` |
| 403 | `FORBIDDEN` |
| 404 | `SUPPLIER_NOT_FOUND` or `SUPPLIER_VERSION_NOT_FOUND` |
| 409 | `SUPPLIER_NAME_CONFLICT` or `SUPPLIER_HAS_ACTIVE_ERRANDS` |
| 500 | `INTERNAL` |
| 503 | `DELETION_FENCE_UNAVAILABLE` or `DEPENDENCY_UNAVAILABLE` |

## Order-service deletion fence

Deletion uses `deletefeature.OrderDeletionFence` with this fail-closed
protocol:

1. The client supplies a UUID `Idempotency-Key`, which becomes the deletion
   operation ID and must be reused for retries of the same logical deletion.
2. Before calling order-service, supplier-service durably creates a
   `requested` operation that binds the operation ID to the supplier ID and a
   bounded maximum-attempt count. Reusing the operation ID for the same
   supplier loads that operation without resetting its state or retry count;
   reusing it for another supplier returns `400 INVALID_ARGUMENT`.
3. A worker must hold the operation's claim lease before calling the fence or
   changing deletion state. Claims are atomic, have a unique claim ID and an
   expiry, and increment the attempt count. An unexpired claim excludes other
   workers; after a crash its expiry makes the operation claimable again.
4. `Acquire(operationId, supplierId)` is idempotent for that pair. It
   atomically blocks creation of new errands for the supplier, then checks
   whether an active errand exists.
5. If `Acquire` has an ambiguous result, supplier-service calls
   `Inspect(operationId)`; it never starts a different operation to guess the
   outcome.
6. If active errands exist, supplier-service records `release_pending`, calls
   `Release(operationId)`, then records `rejected_active_errands` and returns
   `SUPPLIER_HAS_ACTIVE_ERRANDS` without deleting.
7. If none exist, supplier-service atomically soft-deletes its current record
   and changes the operation to `commit_pending`, then calls
   `Commit(operationId)`. Commit makes the order-side block permanent; after
   confirmation the local operation becomes `completed`.
8. If the supplier write fails, supplier-service calls `Release`. If acquire
   is unavailable or ambiguous, deletion returns
   `DELETION_FENCE_UNAVAILABLE`. It never assumes deletion is safe.

The durable state machine is:

| State | Meaning | Allowed next state |
| --- | --- | --- |
| `requested` | Recorded before the first fence call; no local deletion is established | `release_pending`, `commit_pending` |
| `release_pending` | Active errands were found and fence release must be confirmed | `rejected_active_errands` |
| `commit_pending` | The supplier is soft-deleted and fence commit must be confirmed | `completed` |
| `rejected_active_errands` | Terminal rejection after release confirmation | none |
| `completed` | Terminal success after commit confirmation | none |

Each operation retains `attemptCount`, `maxAttempts`, `nextAttemptAt`, lease
metadata, and a bounded failure code such as `acquire_unavailable` or
`commit_unavailable`. Raw dependency errors, headers, tokens, and credentials
are never stored. Claimable-operation queries are bounded, return only due
non-terminal operations with attempts remaining, and use stable operation-ID
ordering. An expired claim may be replaced; every later mutation checks the
current claim ID so the stale worker cannot update the operation.

`Inspect`, `Commit`, and `Release` are idempotent. A commit failure after the
local soft delete leaves the fence closed (safe for new-order creation) and
returns `202 Accepted` with `{operationId, state: "commit_pending"}`. Pending
operations are durably listed after restart and reconciled using the same
operation ID. They are marked complete locally only after order-service
confirms `committed`; a post-delete failure must never release the fence or be
treated as permission to create an errand.

Replays observe or continue the original operation. `completed` returns the
completed `204` result, `commit_pending` returns `202`, and
`rejected_active_errands` returns `409 SUPPLIER_HAS_ACTIVE_ERRANDS`. A replay
in `requested` or `release_pending` returns `503 DELETION_FENCE_UNAVAILABLE`
unless the caller safely resolves and advances it; these pre-delete states
never grant permission to delete.

Order-service must run every errand creation through the same supplier gate and
reject creation while a fence is open or committed. On success it stores both
the selected `supplierId` and `versionId`; later display reads use `versionId`.

## Route ownership

HTTP features implement `httpapi.RouteRegistrar`. The application composition
passes registrars to `httpapi.NewRouter`; feature packages register their own
paths without modifying the central router. The readiness feature is the first
concrete registrar.
