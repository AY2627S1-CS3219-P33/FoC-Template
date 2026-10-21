# Errand creation (A2)

`create` serves `POST /errands` (commit A2, `feat(order-create): create errands
pending reservation`). It validates the submission, resolves the pickup
supplier and delivery building against supplier-service, and stores the errand
in `PENDING_RESERVATION` with its `credit.reserve` and `errand.placed` outbox
rows through [`errand.Store.Create`](../ports.go). Credit-service answers
asynchronously; the errand becomes `OPEN` or `REJECTED` later (F3.1.4).

The handler depends only on ports (`Store`, `SupplierGate`, `Clock`), so it is
tested against [`testfakes`](../../testfakes/) and gets `errandrepo` and B's
supplier client only at integration (I1–I3).

## Request

### Headers

| Header | Required | Rules |
| --- | --- | --- |
| `Authorization` | yes | `Bearer <user token>` with the `errands:use` scope |
| `Idempotency-Key` | yes | A UUID, any letter case; one per errand the client means to create |
| `Content-Type` | no | `application/json`; not checked |

The requester is the token's `sub`. The body never names the requester
(NFR3.3.1); a `requesterId` member is refused like any other unknown field.

### Body

A single JSON object of at most 64 KiB. Unknown members are refused.

| Field | Type | Required | Rules |
| --- | --- | --- | --- |
| `supplierId` | UUID | yes | An existing, open supplier |
| `deliveryBuildingId` | UUID | yes | An existing building that is not retired |
| `deliveryDetail` | string | yes | Not blank; at most 200 characters |
| `items` | array | yes | 1 to 20 items, each `{description, quantity}` |
| `items[i].description` | string | yes | Not blank; at most 200 characters |
| `items[i].quantity` | integer | yes | 1 to 99 |
| `notes` | string | no | At most 500 characters; default `""` |
| `contactName` | string | yes | Not blank; at most 100 characters; no control characters |
| `contactMobile` | string | yes | Not blank; at most 32 characters; no control characters |
| `reward` | integer | yes | 1 to 2,147,483,647; no strings, fractions or exponents |
| `acceptanceDeadline` | RFC 3339 date-time | no | 10 minutes to 24 hours from now, both inclusive; default one hour from now |

`null` counts as absent. Lengths are in characters (Unicode code points), not
bytes.

```http
POST /errands
Authorization: Bearer <user token>
Idempotency-Key: 0d6f3c2e-8b41-4a7e-9c55-7e2a1b9f4d30
Content-Type: application/json
```

```json
{
  "supplierId": "3b9d4e21-7a6f-4c18-8e02-5f1a9c3d7b64",
  "deliveryBuildingId": "c4f8e1a2-5b3d-4e7f-9a6c-1d2b3e4f5a6b",
  "deliveryDetail": "Level 2, seminar room 2",
  "items": [{ "description": "Chicken rice", "quantity": 1 }],
  "notes": "No chilli",
  "contactName": "Alex Tan",
  "contactMobile": "+65 9123 4567",
  "reward": 300
}
```

### Response

`202 Accepted` with `Location: /errands/{errandId}` and a
`RequesterErrandView` body. The errand is not `OPEN` yet: it waits in
`PENDING_RESERVATION` until credit-service reserves the reward.

```json
{
  "errandId": "6f1c2a9e-0b7d-4c3e-9a51-2d8e4f7b1c90",
  "version": 1,
  "status": "PENDING_RESERVATION",
  "supplierId": "3b9d4e21-7a6f-4c18-8e02-5f1a9c3d7b64",
  "supplierVersionId": "a7e2c5d8-1f4b-4a93-b6c0-9d3e8f2a5c17",
  "pickup": {
    "name": "Fine Food",
    "building": "Town Plaza",
    "floor": "1",
    "locationDescription": "Next to the lift lobby",
    "latitude": 1.3045,
    "longitude": 103.7731
  },
  "deliveryBuilding": { "id": "c4f8e1a2-5b3d-4e7f-9a6c-1d2b3e4f5a6b", "name": "COM1" },
  "deliveryDetail": "Level 2, seminar room 2",
  "items": [{ "description": "Chicken rice", "quantity": 1 }],
  "notes": "No chilli",
  "reward": 300,
  "createdAt": "2026-10-20T04:07:12Z",
  "updatedAt": "2026-10-20T04:07:12Z",
  "acceptanceDeadline": "2026-10-20T05:07:12Z",
  "viewerRole": "REQUESTER",
  "contactName": "Alex Tan",
  "contactMobile": "+65 9123 4567"
}
```

Values above are illustrative.

## Requests and responses

Checks run in this order, and the first failure is returned:

1. token and scope;
2. `Idempotency-Key`;
3. **replay check**, before the body is read;
4. body size and JSON shape;
5. body fields;
6. delivery building, against supplier-service;
7. supplier, against supplier-service;
8. deletion fence and insert, in `Store.Create`.

Errors have the body `{"code": "...", "message": "..."}`.
`VALIDATION_FAILED` adds `fields`, one entry per invalid field:

```json
{
  "code": "VALIDATION_FAILED",
  "message": "errand is invalid",
  "fields": [
    { "field": "items[0].quantity", "message": "must be an integer from 1 to 99" },
    { "field": "reward", "message": "must be a positive integer" }
  ]
}
```

### Success

| Request | Response |
| --- | --- |
| Valid body, new `Idempotency-Key` | `202`, `RequesterErrandView` in `PENDING_RESERVATION`, `Location` header |
| No `acceptanceDeadline` | `202`, deadline one hour from now |
| Same `Idempotency-Key` as an earlier request, any body (even an invalid one) | `200`, the original errand in its current state (it may be `OPEN` or `REJECTED` by now); no `Location` |
| Same key from a different user | Treated as new: the errand ID is derived from both the user and the key |
| Two concurrent first requests with the same key | One gets `202`, the other `200` with the same errand |

### Headers and body shape

| Request | Response |
| --- | --- |
| No token, or an invalid one | `401 UNAUTHENTICATED` |
| User token without `errands:use`, or a service token | `403 FORBIDDEN` |
| No `Idempotency-Key` | `400 INVALID_ARGUMENT`: "Idempotency-Key header is required" |
| `Idempotency-Key: abc` | `400 INVALID_ARGUMENT`: "Idempotency-Key must be a UUID" |
| Body over 64 KiB | `400 INVALID_ARGUMENT`: "request body is too large" |
| Empty or whitespace-only body | `400 INVALID_ARGUMENT`: "request body is required" |
| `[]`, `"text"`, `null` or invalid JSON | `400 INVALID_ARGUMENT`: "request body must be a JSON object" |
| `{...} {...}` (more than one value) | `400 INVALID_ARGUMENT`: "request body must be a single JSON object" |

### Body fields

All are `400 VALIDATION_FAILED`, reported together in `fields`.

| Request | `field`: `message` |
| --- | --- |
| A required field missing or `null` | `<name>`: "is required" |
| A text or UUID field that is not a string | `<name>`: "must be a string" |
| `supplierId` or `deliveryBuildingId` not a UUID | `<name>`: "must be a UUID" |
| `deliveryDetail`, `contactName` or `contactMobile` blank | `<name>`: "must not be blank" |
| A text field too long | `<name>`: "must be at most N characters" |
| `contactName` or `contactMobile` with a control character, e.g. `\n` | `<name>`: "must not contain control characters" |
| `items` not an array | `items`: "must be an array" |
| `items: []` | `items`: "must list at least one item" |
| More than 20 items | `items`: "must list at most 20 items" |
| An item that is not an object | `items[i]`: "must be an object" |
| `quantity` of 0, 100, `1.5` or `"2"` | `items[i].quantity`: "must be an integer from 1 to 99" |
| An unknown member in an item | `items[i].<name>`: "is not allowed" |
| `reward` of 0, -5, `1.5`, `1e3`, `"300"` or above 2,147,483,647 | `reward`: "must be a positive integer" |
| `acceptanceDeadline` not RFC 3339 | `acceptanceDeadline`: "must be an RFC 3339 date-time" |
| `acceptanceDeadline` now or in the past | `acceptanceDeadline`: "must be in the future" |
| `acceptanceDeadline` under 10 minutes away | `acceptanceDeadline`: "must be at least 10 minutes from now" |
| `acceptanceDeadline` over 24 hours away | `acceptanceDeadline`: "must be at most 24 hours from now" |
| An unknown top-level member, e.g. `requesterId` | `<name>`: "is not allowed" |

### Supplier-service and store

| Request | Response |
| --- | --- |
| Valid fields, unknown `deliveryBuildingId` | `400 VALIDATION_FAILED`, `deliveryBuildingId`: "must be an existing building" |
| Valid fields, retired building | `400 VALIDATION_FAILED`, `deliveryBuildingId`: "must not be a retired building" |
| Supplier missing, deleted or closed | `422 SUPPLIER_UNAVAILABLE` |
| Supplier has an open or committed deletion fence | `409 SUPPLIER_DELETION_IN_PROGRESS` |
| Supplier-service unreachable | `503 DEPENDENCY_UNAVAILABLE` |
| Replay of an errand whose supplier version is unknown | `500 INTERNAL` (see [browse](../browse/README.md#an-unknown-supplier-version-hides-the-errand-on-the-list)) |
| Any unexpected failure (store error, etc.) | `500 INTERNAL`; logged with the errand ID only |

## Design decisions

### Derived errand IDs

The errand ID is a UUIDv5 hash of the requester and their `Idempotency-Key`
(`id.go`), not a random UUID.

**Why.** The contract (`docs/contracts.md`, "Idempotency keys") says a replay
returns the original errand "whatever the replay's body contains". That means
the replay has to be recognised **before** the body is validated and before
supplier-service is called. Otherwise a replay could get `400` for a changed
body, or `422` because the supplier has since closed. The `Store` port has no
lookup by idempotency key, and adding one would be a frozen-contract change.
With a derived ID, the replay check is a plain `Store.Get`.

`Store.Create` still enforces the `(requester_id, create_idempotency_key)`
unique constraint, so two concurrent first requests produce one errand. The
loser gets `created = false` and answers `200` with the winner's errand.

**Consequences.**

- Errand IDs must never be generated any other way for this route.
- `errandNamespace` in `id.go` must never change, or replays of earlier
  requests stop finding their errands.
- IDs are predictable only to someone who knows both the requester's `sub` and
  the key, which is the requester's own client. Nothing can be inferred from an
  ID, because the hash is one-way.

### Shared response package

The `RequesterErrandView` response and the error helpers live in
[`errandhttp`](../errandhttp/), not here, because A3 (`GET /errands/{id}`) and
A5 (edit, cancel, confirm, dispute) return the same view. This path is not in
the package layout in `commit-plan.md`. It is under Person A's
`internal/errand/` subtree, but B and C should know it exists.

### Building check comes after field validation

Field errors are reported together, but an unknown or retired delivery
building is reported only once the other fields are valid. Checking the
building needs a call to supplier-service, which is not worth making for a body
that is already invalid. A request with both problems therefore takes two
round trips to fix.

### Validation is stricter than `errand.Next`

`input.go` applies the full `ErrandCreate` schema: item shape and limits, text
lengths, unknown fields, an integer reward within `int32`. `errand.Next` checks
only the domain rules (reward, at least one item, contact, deadline). Both run;
`input.go` reports first, with field paths.

## Open concerns

- **`rejectionReason` is never returned.** `RequesterErrandView` has an
  optional `rejectionReason` for `REJECTED` errands (`INSUFFICIENT_CREDITS`,
  `RESERVATION_FAILED`, and others). The reason is recorded on the `REJECTED`
  transition, but the `Store` port exposes neither transitions nor a rejection
  reason on `errand.Errand`. A replay of a rejected errand therefore shows
  `status: REJECTED` without saying why. A3's `GET /errands/{id}` has the same
  gap; closing it needs a jointly reviewed port change (for example a
  `RejectionReason` field on `Errand`, or a store read of the last
  transition's reason).
- **Not wired into the service.** There is no `cmd/order-service` or
  `internal/app` yet. `create.NewHandler` is registered in the integration
  commits (I1–I3).
- **Race detector not run.** `go test -race` needs cgo, which was not
  available on the machine the tests were written on. The handler holds no
  shared mutable state, but run `go test -race ./internal/errand/create/` in CI
  or on Linux.
- **`gofmt -l` flags the S0 files.** It lists the existing
  `internal/errand/*.go` files, most likely because of CRLF line endings from a
  Windows checkout. The A2 files are not flagged, and the S0 files were left
  unchanged.
