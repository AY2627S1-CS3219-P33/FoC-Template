# Accepting errands (A4)

`accept` serves `POST /errands/{errandId}/accept` (commit A4,
`feat(order-accept): accept errands with eligibility and version checks`):
`OPEN → ASSIGNED` for an eligible courier (F3.3, F3.3.1–F3.3.7, F3.6.3,
F3.6.4).

The handler gathers what `errand.Next` needs and persists its decision with
`Store.Apply`. Next decides everything else:

- the eligibility errors;
- the deadline guard;
- the pickup and delivery deadlines;
- the reverting `credit.adjust` when a reward edit is pending.

Exactly one of competing accepts, cancels and expiries wins, through the
store's version compare-and-swap (F3.3.5, tested on Postgres in A1).

The handler depends only on ports (`Store`, `SupplierGate`, `Settings`,
`Clock`) and is tested against [`testfakes`](../../testfakes/).

## Request

| Part | Name | Required | Rules |
| --- | --- | --- | --- |
| header | `Authorization` | yes | `Bearer <user token>` with the `errands:use` scope; the courier is the token's `sub` |
| path | `errandId` | yes | A UUID, any letter case |
| body | `expectedVersion` | yes | A positive integer: the `version` the courier last saw, from the open list or `GET /errands/{errandId}` |

The body is a single JSON object of at most 4 KiB, with `expectedVersion` and
nothing else. There is no `Idempotency-Key`; a retry is recognised from the
errand itself (see [below](#retrying-a-committed-accept-answers-200)).

```http
POST /errands/6f1c2a9e-0b7d-4c3e-9a51-2d8e4f7b1c90/accept
Authorization: Bearer <user token>
Content-Type: application/json

{ "expectedVersion": 1 }
```

### Response

`200 OK` with `CourierErrandView`. The errand is now `ASSIGNED` to the caller
at `expectedVersion + 1`, with its pickup and delivery deadlines set from the
settings. The contact person is included, and the requester's identity never
is.

```json
{
  "errandId": "6f1c2a9e-0b7d-4c3e-9a51-2d8e4f7b1c90",
  "version": 2,
  "status": "ASSIGNED",
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
  "updatedAt": "2026-10-20T04:12:40Z",
  "acceptanceDeadline": "2026-10-20T05:07:12Z",
  "pickupDeadline": "2026-10-20T04:42:40Z",
  "deliveryDeadline": "2026-10-20T05:12:40Z",
  "viewerRole": "COURIER",
  "contactName": "Alex Tan",
  "contactMobile": "+65 9123 4567"
}
```

Values above are illustrative; the deadlines depend on the settings.

## Requests and responses

Checks run in this order, and the first failure is returned:

1. token and scope;
2. `errandId`;
3. body;
4. errand lookup, then the **retry check**;
5. settings and courier history reads;
6. errand status (`errand.Next`);
7. eligibility of the caller (`errand.Next`);
8. acceptance deadline (`errand.Next`);
9. `expectedVersion` against the errand's version;
10. compare-and-swap in `Store.Apply`.

So a requester trying to accept their own `ASSIGNED` errand gets `409
ERRAND_UNAVAILABLE` (step 6), not `403 NOT_ELIGIBLE` (step 7). An ineligible
courier with a stale version gets `403` (step 7), not `409 ERRAND_CHANGED`
(step 9).

Errors have the body `{"code": "...", "message": "..."}`;
`VALIDATION_FAILED` adds `fields`. **Announced** means `Store.RecordRejection`
also writes an `event.errand.accept_rejected` outbox row so the courier is
notified (`docs/contracts.md`, "Rejection events"). A retry of the same
refused request, by the same courier with the same `expectedVersion`, is
announced once.

### Success

| Request | Response |
| --- | --- |
| Eligible courier, errand `OPEN`, before the acceptance deadline, current `expectedVersion` | `200`, `CourierErrandView` in `ASSIGNED` |
| Errand has a pending reward edit | `200`; the edit is dropped and a reverting `credit.adjust` is queued |
| Retry of an accept that committed (errand `ASSIGNED` to the caller at `expectedVersion + 1`) | `200`, the same view; nothing is written |

### Path and body

| Request | Response |
| --- | --- |
| No token, or an invalid one | `401 UNAUTHENTICATED` |
| User token without `errands:use`, or a service token | `403 FORBIDDEN` |
| `errandId` not a UUID | `400 INVALID_ARGUMENT`: "errandId must be a UUID" |
| Body over 4 KiB | `400 INVALID_ARGUMENT`: "request body is too large" |
| Empty or whitespace-only body | `400 INVALID_ARGUMENT`: "request body is required" |
| `[]`, `"text"`, `null` or invalid JSON | `400 INVALID_ARGUMENT`: "request body must be a JSON object" |
| `{...} {...}` (more than one value) | `400 INVALID_ARGUMENT`: "request body must be a single JSON object" |
| `{}` or `{"expectedVersion": null}` | `400 VALIDATION_FAILED`, `expectedVersion`: "is required" |
| `expectedVersion` of 0, -1, `1.5` or `"1"` | `400 VALIDATION_FAILED`, `expectedVersion`: "must be a positive integer" |
| Any other member, e.g. `courierId` | `400 VALIDATION_FAILED`, `<name>`: "is not allowed" |

### Errand state and eligibility

| Request | Response | Announced |
| --- | --- | --- |
| No errand with that ID | `404 ERRAND_NOT_FOUND` | no |
| Errand still `PENDING_RESERVATION` | `409 INVALID_TRANSITION`: "cannot accept an errand that is PENDING_RESERVATION" | no |
| Errand already accepted by someone else, or later in its lifecycle | `409 ERRAND_UNAVAILABLE` | **yes** |
| Errand `CANCELLED`, `EXPIRED`, `REJECTED` or otherwise terminal | `409 ERRAND_UNAVAILABLE` | **yes** |
| Caller is the requester | `403 NOT_ELIGIBLE`: "you cannot accept your own errand" | no |
| Caller withdrew from this errand before, voluntarily or forced | `403 NOT_ELIGIBLE`: "you cannot accept an errand you withdrew from" | no |
| Caller has `count` or more withdrawals within the restriction `window` | `403 COURIER_RESTRICTED`: "you are temporarily restricted from accepting errands" | no |
| At or after the acceptance deadline, before the expiry job ran | `409 ERRAND_UNAVAILABLE`: "the errand's acceptance deadline has passed" | **yes** |
| Errand still `OPEN` but `expectedVersion` is stale (e.g. the requester edited it) | `409 ERRAND_CHANGED`: "the errand has changed; reload it and try again" | **yes** |
| Another accept, cancel or expiry commits first (lost compare-and-swap) | `409 ERRAND_UNAVAILABLE` | **yes** |
| Version changed between the read and the write, errand still `OPEN` | `409 ERRAND_CHANGED` | **yes** |

### Dependencies

| Request | Response |
| --- | --- |
| Settings or courier history unreadable | `500 INTERNAL`; nothing is written |
| Supplier-service unreachable while building the response | `503 DEPENDENCY_UNAVAILABLE`, **but the accept has committed**; a retry gets `200` |
| Recording the rejection event fails | The `409` is still returned; the failure is logged |
| Any other unexpected failure | `500 INTERNAL`; logged with the errand ID only |

## Design decisions

### Rejection request ID is derived, not taken from a header

The rejection event's key is `{errandId}:accept-rejected:{requestId}`, and the
contract wants "a client retry of the same refused request" to notify once.
The service has no request-ID header or middleware, so
`errandhttp.RejectionRequestID` derives `requestId` from a SHA-256 of
(courier, `expectedVersion`):

- a retry by the same courier with the same version gets the same key, so the
  outbox insert is a no-op;
- another courier, or the same courier with a newer version, is announced
  separately.

A5 (cancel) and A6 (pickup) should use the same helper.

### Stale version is checked after `errand.Next`

`errand.Next` sets `ExpectedVersion` to the errand's current version; it does
not know the client's `expectedVersion`. The handler compares them after Next
runs, so an errand that has moved on (`ASSIGNED` to someone else, `EXPIRED`)
is reported as `ERRAND_UNAVAILABLE`, not as a stale version. A stale version
on a still-`OPEN` errand is `ERRAND_CHANGED`. A 403 eligibility refusal wins
over a stale version and over the deadline, because Next checks the actor
before its guards; it does not win over a status that has moved on, which
Next checks first.

### Retrying a committed accept answers 200

The accept commits before the response is built, and building it calls
supplier-service for the pickup location. If that call fails, the client gets
`503` for an accept that succeeded. Without the retry check, its retry would
find the errand `ASSIGNED` and get `409 ERRAND_UNAVAILABLE`, and would also
notify the courier that their own accept was rejected.

The retry check recognises the retry safely: only an accept moves an errand
from `OPEN` to `ASSIGNED`, and it adds exactly one to the version, so
`ASSIGNED` to the caller at `expectedVersion + 1` can only be the result of
this accept.

### The rejection notification is best effort

If `RecordRejection` fails, the error is logged and the courier still gets
the `409`. Turning it into a `500` would hide the real answer, and the client
cannot fix a failed notification by retrying.

## Open concerns

- **The restriction's `duration` is not applied.** The setting is "`count`
  withdrawals within `window` restrict the courier for `duration`" (F3.6.4).
  `Store.CourierHistory` returns only a count since a time, so, following
  `commit-plan.md`, the courier is restricted while `count` withdrawals fall
  within the last `window`. The restriction therefore lifts when the oldest
  of those withdrawals leaves the window, not `duration` after the
  withdrawal that triggered it. With the defaults (`window` = `duration` = 15
  minutes) a courier who withdraws three times in quick succession is
  restricted for about 15 minutes, as intended. If an admin sets `duration`
  above `window`, the restriction ends early. Applying `duration` exactly
  needs the withdrawal times from the store, which is a port change. B's
  F6.3.1 flag report should use the same rule as this check.
- **A refused accept costs extra reads.** Settings and courier history are
  read before Next, even when the errand turns out not to be `OPEN`. Settings
  reads are expected to be cheap or cached (B1); `CourierHistory` uses
  `errand_transitions_withdrawals_idx`.
- **Not wired into the service.** `accept.NewHandler` is registered in the
  integration commits (I1–I3).
- **Race detector not run** (no cgo on the development machine). The handler
  holds no shared mutable state.
