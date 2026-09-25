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
validation.

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

All supplier and version endpoints require authentication. Create, patch, and
delete additionally require `suppliers:manage`, currently granted to the
`administrator` role. `/readyz` is unauthenticated. Supplier use cases consume
the trusted `auth.Principal`; an `auth.Port` adapter owns token, session, or
user-service verification. Payload identity and role values are ignored.

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

1. `Acquire(supplierId)` atomically blocks creation of new errands for that
   supplier, then checks whether an active errand exists.
2. If active errands exist, supplier-service calls `Release(fenceId)` and
   returns `SUPPLIER_HAS_ACTIVE_ERRANDS` without deleting.
3. If none exist, supplier-service soft-deletes its current record and calls
   `Commit(fenceId)`. Commit makes the order-side block permanent.
4. If the supplier write fails, supplier-service calls `Release`. If acquire
   is unavailable or ambiguous, deletion returns
   `DELETION_FENCE_UNAVAILABLE`. It never assumes deletion is safe.

`Commit` and `Release` are idempotent. A commit failure after the local soft
delete leaves the fence closed (safe for new-order creation) and is retried or
reconciled; it must not be treated as permission to create an errand.

Order-service must run every errand creation through the same supplier gate and
reject creation while a fence is open or committed. On success it stores both
the selected `supplierId` and `versionId`; later display reads use `versionId`.

## Route ownership

HTTP features implement `httpapi.RouteRegistrar`. The application composition
passes registrars to `httpapi.NewRouter`; feature packages register their own
paths without modifying the central router. The readiness feature is the first
concrete registrar.
