# Requester actions (A5)

`requester` serves the requester's four errand actions (commit A5,
`feat(order-requester): edit, cancel, confirm and dispute`):

| Endpoint | Transition | Credit effect |
| --- | --- | --- |
| `PATCH /errands/{errandId}` | `OPEN → OPEN` (F3.4) | none, or `credit.adjust` when the reward changes (F4.4) |
| `POST /errands/{errandId}/cancel` | `OPEN → CANCELLED` (F3.5) | `credit.release` (F4.5.1) |
| `POST /errands/{errandId}/confirm` | `DELIVERED → COMPLETED` (F3.10.1) | `credit.transfer` to the courier (F4.2.2) |
| `POST /errands/{errandId}/dispute` | `DELIVERED → REVIEW_REQUIRED` (F3.10) | none; the reservation stays held |

As in A4, `errand.Next` decides every transition and its outbox rows, and
`Store.Apply` persists it with a compare-and-swap. The handler reads the body
and the errand, checks `expectedVersion`, and talks to supplier-service when
an edit changes the supplier or building.

The handler depends only on ports (`Store`, `SupplierGate`, `Clock`) and is
tested against [`testfakes`](../../testfakes/).

## Editing

The body is `ErrandEdit`: `expectedVersion` plus at least one field to
change. Omitted or `null` fields keep their current value. The server builds
the complete edited errand and validates all of it with the same field rules
as creation (shared through `errandhttp.Validator`).

```http
PATCH /errands/6f1c2a9e-0b7d-4c3e-9a51-2d8e4f7b1c90
Authorization: Bearer <user token>
Content-Type: application/json

{ "expectedVersion": 2, "reward": 400, "notes": "No chilli, extra rice" }
```

There are two outcomes:

- **Reward unchanged:** `200`, applied at once, version + 1, `errand.edited`.
- **Reward changed (up or down):** `202`. The *whole* edit is stored as
  `pendingEdit` and nothing is applied yet. One `credit.adjust` row is written
  with the new absolute amount, keyed `{errandId}:adjust:{version}`, plus
  `errand.edit_requested`. **The version does not change**, so couriers keep
  seeing, and can accept, the current details.

The pending edit ends in one of four ways, each a transition that bumps the
version:

| What happens | Effect |
| --- | --- |
| `credit.adjusted` (M4) | The whole edit is applied |
| `credit.adjust_refused` (M4), or no result in time (M5) | The edit is dropped; a time-out also sends a reverting adjust |
| A courier accepts (A4) | The accept wins: the edit is dropped and a reverting adjust is sent (F3.4.2) |
| The requester cancels, or the errand expires | The edit is dropped; the release returns the whole reservation |

Edits are only possible while `OPEN`. Once a courier accepts, the details
are fixed: a later `PATCH` is `409 ERRAND_UNAVAILABLE`, and a late credit
result finds no pending edit and is ignored.

### Supplier and building

| Field | When it changes |
| --- | --- |
| `supplierId` | Resolved to the supplier's current version with `SupplierGate.ResolveForCreate`, which must be available and open now (`422 SUPPLIER_UNAVAILABLE` otherwise). `Store.Apply` refuses a supplier being deleted (`409 SUPPLIER_DELETION_IN_PROGRESS`). |
| `deliveryBuildingId` | Must exist and not be retired (`400 VALIDATION_FAILED`); its snapshot is taken again. |

Sending the errand's current `supplierId` is not a change: the stored
supplier version is kept even if the supplier has published a newer one.

### Acceptance deadline

A changed `acceptanceDeadline` must be 10 minutes to 24 hours from now
(F3.1.3). An unchanged one only has to be in the future, so editing the notes
still works when the deadline is less than 10 minutes away.

## Cancel, confirm and dispute

The bodies are `VersionedAction` (`{"expectedVersion": n}`) and, for dispute,
`ReasonedAction` (`expectedVersion` plus a required, non-blank `reason` of at
most 500 characters). The reason becomes the review's note. All three answer
`200` with `RequesterErrandView`.

### Retries answer 200

Each action recognises its own committed result, so a retry whose response
was lost gets `200` again, writes nothing and, for cancel, is not announced as
a rejection:

| Action | Recognised when the errand is |
| --- | --- |
| cancel | `CANCELLED` at `expectedVersion + 1` (only a cancel reaches `CANCELLED`) |
| confirm | `COMPLETED`, whatever `expectedVersion` says (F3.10.2), including after auto-complete or an admin's resolution |
| dispute | `REVIEW_REQUIRED` with `REQUESTER_DISPUTE` at `expectedVersion + 1` |

## Order of checks

1. token and scope (`401`, `403`);
2. `errandId` (`400 INVALID_ARGUMENT`);
3. body (`400`);
4. errand lookup (`404 ERRAND_NOT_FOUND`), then **ownership**: anyone but the
   requester gets `403 FORBIDDEN` whatever the state;
5. the retry check (cancel, confirm, dispute);
6. `errand.Next`: state (`409 ERRAND_UNAVAILABLE` / `INVALID_TRANSITION`),
   `409 EDIT_PENDING`, edit validation (`400`);
7. `expectedVersion` (`409 ERRAND_CHANGED`);
8. supplier-service: supplier and building for an edit, then the supplier
   versions for the response;
9. compare-and-swap in `Store.Apply`.

## Rejection events

Only **cancel** is announced. A `409 ERRAND_CHANGED`, `ERRAND_UNAVAILABLE` or
`INVALID_TRANSITION` refusal also writes an `event.errand.cancel_rejected` row
through `Store.RecordRejection` (`docs/contracts.md`, "Rejection events"). The
request ID comes from `errandhttp.RejectionRequestID`, as in A4, so a retry of
the same refused request notifies once. The notification is best effort: if
it fails, the error is logged and the `409` is still returned.

Edit, confirm and dispute refusals are not announced; the contract defines no
event for them. `403` refusals are never announced.

## Design decisions

### Ownership is checked before the state

`errand.Next` checks the errand's state before the actor, which suits accept,
where any courier may act. For requester-only actions the handler checks
ownership first:

- the state of someone else's errand is not revealed;
- a stranger cannot fill the outbox with `cancel_rejected` events by calling
  cancel with different versions.

`errand.Next` still checks the relationship as well.

### Supplier-service is read before the write

The response needs the pickup locations, which come from supplier-service.
The handler reads them before `Store.Apply`, so a supplier-service failure
answers `503` with nothing written, instead of `503` for a change that has
committed (the A4 retry problem).

### Edits are decided before supplier-service is called

The edit is run through `errand.Next` once with the merged details before any
supplier or building lookup, and again with the resolved details before
writing. A refused edit (wrong state, edit pending, stale version, invalid
field) therefore costs no remote call. `Next` is pure, so running it twice
has no side effects.

## Open concerns

- **An edit has no retry check.** If the connection drops after an edit
  commits, the retry gets `409 ERRAND_CHANGED` (applied) or `409
  EDIT_PENDING` (pending). Recognising it would mean comparing the requested
  details with the stored ones, and Postgres `jsonb` does not keep the bytes
  of `items`. The client recovers by reloading the errand, and nothing is
  announced.
- **Not wired into the service.** `requester.NewHandler` is registered in the
  integration commits (I1–I3).
- **Race detector not run** (no cgo on the development machine). The handler
  holds no shared mutable state.
