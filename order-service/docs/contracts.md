# Order-service contracts

This document records the decisions order-service shares with its own three
work streams (A, B, C in `references/commit-plan.md`) and with supplier-,
user-, credit- and notification-service. The HTTP contract is
[openapi.yaml](../openapi.yaml); the Go side of these rules is
`internal/errand` (`ports.go`, `outbox.go`, `state.go`). Errand lifecycle
behaviour is Diagram 1 in `references/order-requirements.md`.

Sections that other teams still have to agree are marked with their gate
(C1–C6 in `commit-plan.md`). Until a gate closes, the text here is the
proposal order-service builds against.

## Contents

1. [Authorization and errors](#authorization-and-errors)
2. [Field visibility](#field-visibility)
3. [Idempotency keys](#idempotency-keys)
4. [Outbox](#outbox)
5. [Transition and review rows](#transition-and-review-rows)
6. [Fence and creation locking](#fence-and-creation-locking)
7. [RabbitMQ contract](#rabbitmq-contract)
8. [Errand events](#errand-events)
9. [Credit message contract (proposed, gate C1)](#credit-message-contract-proposed-gate-c1)
10. [Cache contract](#cache-contract)
11. [Internal routes](#internal-routes)

## Authorization and errors

Every route except `/readyz` needs an Auth0 RS256 access token for the
audience `https://api.foc.local/order-service`, validated locally against
cached JWKS (`internal/auth`). Identity is the token's `sub`; identity or role
fields in a request are ignored (NFR3.3.1).

| Route group | Token kind | Permission (gate C3) |
| --- | --- | --- |
| `/errands/**`, `/me/errands/**` | user | `errands:use` |
| `/admin/**` | user | `errands:review` |
| `/internal/**` | service (`gty: client-credentials`) | `orders:internal` |
| `/readyz` | none | none |

A service token on a user or admin route, or a user token on an internal
route, is `403 FORBIDDEN` whatever permissions it carries (NFR3.5.3).

On top of the permission, every errand action checks the caller's
relationship to the errand against stored data (NFR3.3.2, `errand.Next`).
OpenAPI records it per operation as `x-required-relationship`:

| Relationship | Meaning | Actions |
| --- | --- | --- |
| requester | `sub` = `errands.requester_id` | edit, cancel, confirm, dispute |
| eligible courier | not the requester, never withdrew from this errand, not restricted (F3.3.6) | accept |
| assigned courier | `sub` = `errands.courier_id` | withdraw, pickup, deliver, report issue |
| admin | `errands:review` | resolve review |
| system | scheduler, credit-result consumer, reconcilers | deadline, reservation and edit transitions |

Error bodies are `{code, message, fields?}` with the stable codes from
`internal/apperror`. `VALIDATION_FAILED` lists every invalid body field in
`fields` (NFR2.2.1); `INVALID_ARGUMENT` is a malformed path, query, header or
cursor.

| HTTP | Codes |
| --- | --- |
| 400 | `INVALID_ARGUMENT`, `VALIDATION_FAILED` |
| 401 | `UNAUTHENTICATED` |
| 403 | `FORBIDDEN`, `NOT_ELIGIBLE`, `COURIER_RESTRICTED` |
| 404 | `ERRAND_NOT_FOUND`, `FENCE_NOT_FOUND`, `OUTBOX_ROW_NOT_FOUND` |
| 409 | `ERRAND_CHANGED`, `ERRAND_UNAVAILABLE`, `INVALID_TRANSITION`, `EDIT_PENDING`, `SUPPLIER_DELETION_IN_PROGRESS` |
| 422 | `SUPPLIER_UNAVAILABLE` |
| 500 | `INTERNAL` |
| 503 | `DEPENDENCY_UNAVAILABLE` |

`INSUFFICIENT_CREDITS` is never an HTTP error: credit-service answers
asynchronously, so it appears as `rejectionReason` on a `REJECTED` errand and
as the `reason` of `errand.rejected` and `errand.edit_rejected`.

How the 409s divide (F3.3.7):

- `ERRAND_CHANGED`: the state allows the action but `expectedVersion` is stale.
- `ERRAND_UNAVAILABLE`: the errand has moved past every state the action can
  leave (accepted by someone else, cancelled, expired, terminal).
- `INVALID_TRANSITION`: the action is not allowed from the state yet, for
  example confirming before delivery. Fence commit/release and outbox requeue
  use it for a row in the wrong state.

## Field visibility

NFR3.6.4 is enforced by giving each viewer its own response schema, so a
field that a viewer must not see does not exist in that schema.

| Schema | Who gets it | Requester identity | Notes | Contact person |
| --- | --- | --- | --- | --- |
| `OpenErrandItem` | any user, `GET /errands/open` | never | never | never |
| `PublicErrandView` | `GET /errands/{id}` by anyone else, only while `OPEN` | never | never | never |
| `RequesterErrandView` | the requester | (own) | yes | yes (their own choice) |
| `CourierErrandView` | the assigned courier | never | yes (F3.12) | only while `ASSIGNED`, `PICKED_UP`, `DELIVERED` or `REVIEW_REQUIRED` |
| `AdminErrandView` | admins, review list and resolution | `requesterId` and `courierId` only | yes | never |

`GET /errands/{id}` returns `ERRAND_NOT_FOUND` to anyone who is neither the
requester nor the assigned courier once the errand has left `OPEN`, so the
existence of an assigned errand is not disclosed. A courier who withdrew is
no longer the assigned courier.

**Contact is one-way, courier to requester.** The requester names a contact
person (`contactName`, `contactMobile`) on creation and may change it while
the errand is `OPEN`, like any other field. It is stored on the errand, need
not be the requester, and is not checked against user-service. The requester
is shown nothing about the courier besides the status: the courier's mobile
is not needed for any requester operation (NFR3.6.3), because status, events
(F5.1), deadlines (F3.8) and problem reports (F3.10) cover it. Order-service
therefore never calls user-service.

The contact person is never logged, never cached and never published:
`errand.edit_requested` carries `pendingEdit` without `contactName` and
`contactMobile`.

## Idempotency keys

Every outbox row has a unique `idempotency_key`, which is also the message's
`messageId` and AMQP `MessageId`. The general format is
`{errandId}:{operation}[:{version}]`; the version is omitted for operations
that happen at most once per errand (`errand.IdempotencyKey`).

| Row | Key | Written by |
| --- | --- | --- |
| `credit.reserve` | `{errandId}:reserve` | `Store.Create` |
| `credit.release` | `{errandId}:release` | `Store.Apply` (cancel, expiry, refund, reservation failed) |
| `credit.transfer` | `{errandId}:transfer` | `Store.Apply` (confirm, verification lapse, admin complete) |
| `credit.adjust` for a reward edit | `{errandId}:adjust:{version}` (the version the edit was made against) | `Store.Apply` (edit) |
| `credit.adjust` reverting a dropped edit | `{errandId}:adjust-revert:{newVersion}` | `Store.Apply` (accept, timed-out edit refusal) |
| `event.errand.<name>` | `{errandId}:<name>:{newVersion}`, e.g. `…:picked_up:5` | `Store.Create`, `Store.Apply` |
| `event.errand.<action>_rejected` | `{errandId}:<action>-rejected:{requestId}` | `Store.RecordRejection` |
| `event.errand.verification_reminder` | `{errandId}:verify-reminder:{version}` | B's scheduler |
| `event.errand.pickup_deadline_approaching` | `{errandId}:pickup-reminder:{version}` | B's scheduler |

Release and transfer carry no version because an errand reaches a terminal
state once, so a second release or transfer row for the same errand can only
be a bug, and the unique constraint rejects it. The pending edit's
`pending_edit_key` stores its adjust key; credit results are matched against
it.

`POST /errands` takes a client-generated UUID `Idempotency-Key` header,
stored as `errands.create_idempotency_key` and unique per requester. A replay
returns `200` with the errand the first request created, whatever the
replay's body contains (NFR7.3). The rejection-event `requestId` is the HTTP
request ID, so a client retry of the same refused request notifies once.

## Outbox

The `outbox` table is the only way order-service sends anything to the
broker. Rows are inserted in the same transaction as the state change they
describe (F4.6.2, F4.6.3, NFR7.4), then published by C's dispatcher.

### Who touches which rows

| Stream | Inserts | Updates |
| --- | --- | --- |
| A (`Store.Create`, `Store.Apply`, `Store.RecordRejection`) | transition rows and rejection rows | never |
| B (scheduler, B4b) | reminder rows only, `ON CONFLICT (idempotency_key) DO NOTHING` | never |
| C (dispatcher, consumer, admin replay) | never | every row: claim, `SENT`, `CONFIRMED`, `FAILED`, requeue |

Rows are never deleted.

### Lifecycle

```text
                   claim (lease, attempts+1)            credit.<result> received
  PENDING ──────────────────────────────▶ (leased) ──publish confirmed──▶ SENT ─────────────────────▶ CONFIRMED   (credit.* only)
     ▲  ▲                                    │                              │  │
     │  └──── Reschedule (backoff) ◀─────────┤ publish failed               │  └─ credit.operation_failed ─▶ FAILED
     │                                       │                              │
     │                                       └─ attempts exhausted, or ─────┼──────────────────────────────▶ FAILED
     │                                          ErrUnpublishable            │
     ├──── unconfirmed sweep: SENT credit row with no result after ◀────────┘
     │     the confirm timeout (same key, re-sent)
     └──── admin requeue of a FAILED row (same key, attempts reset, audited)
```

- **Claim.** One statement selects `status = 'PENDING' AND next_attempt_at
  <= now() AND (lease_expires_at IS NULL OR lease_expires_at < now())` for
  the loop's kinds, ordered by `next_attempt_at`, `FOR UPDATE SKIP LOCKED`,
  and sets `lease_owner` (the worker ID), `lease_expires_at` and
  `attempts = attempts + 1`. Credit rows and event rows are claimed by
  separate loops so an event backlog never delays credit commands.
- **Lease.** `MarkSent`, `Reschedule` and `MarkFailed` succeed only
  `WHERE lease_owner = $worker`; a worker that lost its lease changes
  nothing. A worker that crashes after publishing but before `MarkSent`
  leaves the lease to expire, and the row is published again with the same
  `messageId`.
- **Retries.** A failed publish reschedules with exponential backoff and full
  jitter (`internal/resilience`, base 2 s, cap 5 min). After the configured
  maximum attempts the row becomes `FAILED` with `last_failure_code`
  (NFR7.3.1, NFR7.4.1). Attempts count claims, i.e. publish attempts, not
  credit-service answers.
- **Event rows** end at `SENT`. Delivery to consumers is at-least-once.
- **Credit rows** go on from `SENT`. The matching result moves them to
  `CONFIRMED` (`MarkConfirmedByKey`; a repeat is a no-op).
  `credit.operation_failed` moves them to `FAILED` for an admin and they are
  not retried. A `SENT` credit row with no result after
  `outboxConfirmTimeout` (default 10 min) goes back to `PENDING` and is sent
  again with the same key; credit-service answers it idempotently. This is
  how release and transfer are retried until confirmed.
- **Replay.** `POST /admin/outbox/{id}/requeue` resets a `FAILED` row to
  `PENDING` with `attempts = 0`, keeping its key, so the replay cannot apply
  an effect twice (NFR7.4.2). It writes an `admin_audit` row.
- **Payloads.** A credit row's payload is an `errand.CreditCommand`; an event
  row's payload is the envelope's `data` (below). Payloads are never returned
  by the admin API because they hold user IDs.

## Transition and review rows

`Store` writes these in the same transaction as the errand row and its
outbox rows (`Create`, `Apply`, and each transition `ClaimDue` applies), so
history and the review queue can never disagree with the errand.

### `errand_transitions.courier_id`

Every transition row records the errand's courier: `courier_id` before the
transition, or after it when there was none before (an accept). It is NULL
for an errand with no courier. `actor_id` stays "who did it", so it is NULL
for `SYSTEM` transitions, including a forced withdrawal; `courier_id` is what
attributes that withdrawal to the courier who was removed.

`Store.CourierHistory` (F3.3.6, F3.6.3, F3.6.4) counts rows with
`courier_id = $courier` and `event IN ('withdraw', 'pickup_deadline_passed')`,
using `errand_transitions_withdrawals_idx`.

### `errand_reviews`

Stream A's `Store` writes review rows; stream B reads them (the admin review
list) and resolves them through `Store.Apply`.

| Transition | Review row |
| --- | --- |
| Into `REVIEW_REQUIRED` (`report_issue`, `dispute`, `delivery_deadline_passed`) | Insert: `reason` = the errand's new `review_reason`; `reporter_id` = actor ID, NULL for `SYSTEM`; `note` = the transition reason, NULL when blank; `created_at` = transition time |
| Out of `REVIEW_REQUIRED` (`admin_complete`, `admin_refund`) | Update the errand's open review: `resolution` = the new status (`COMPLETED`/`REFUNDED`); `resolver_id` = admin ID; `resolution_reason` = the transition reason (required, F6.1.4); `resolved_at` = transition time |

An open review is one with `resolution IS NULL`. `errand_reviews_open_idx`
allows at most one per errand, because an errand is in `REVIEW_REQUIRED` at
most once at a time. Resolving an errand with no open review is a bug and
rolls the transaction back. The admin audit row for a resolution (F6.5) is
still B's, written after `Apply` commits.

## Fence and creation locking

This is order-service's side of supplier-service's
[Order-service deletion fence](../../supplier-service/docs/contracts.md#order-service-deletion-fence).
The fence must make it impossible for an errand to be created for, or edited
onto, a supplier that supplier-service has just checked and found to have no
active errands.

### Lock key

Both sides lock the same transaction-scoped PostgreSQL advisory lock:

```sql
hashtextextended('supplier:' || $supplier_id::text, 0)
```

`$supplier_id` is the `uuid`, so `::text` is always the canonical lower-case
form and both sides derive the same `bigint`. A hash collision between two
suppliers only makes their creations and fences wait for each other; it
never changes an answer. Both locks are `xact` locks, so they are released at
commit or rollback and can't leak.

### Errand creation (A1, `Store.Create`)

In one `READ COMMITTED` transaction:

1. `SELECT pg_advisory_xact_lock_shared(hashtextextended('supplier:' || $1::text, 0));`
2. `SELECT 1 FROM supplier_fences WHERE supplier_id = $1 AND state IN ('OPEN', 'COMMITTED') LIMIT 1;`
   If a row exists: roll back, `409 SUPPLIER_DELETION_IN_PROGRESS`.
3. Insert the errand (`PENDING_RESERVATION`), its transition row, and its
   `credit.reserve` and `errand.placed` outbox rows.
4. Commit.

Shared locks don't block each other, so creations for one supplier run in
parallel. `SupplierGate.ResolveForCreate` (deleted, closed) is checked before
the transaction; the fence check inside it is what closes the race.

### Supplier-changing edits (A1, `Store.Apply`)

An `edit` transition whose new details have a different `supplierId` from
the stored errand is fenced the same way. This covers both kinds of edit: one
applied at once (`Changes.Details`) and a reward edit stored as
`pending_edit` (`Changes.PendingEdit`). In the `Apply` transaction, before
the compare-and-swap `UPDATE`:

1. Take the shared lock on the **new** supplier's key.
2. Run the creation step 2 query for the new supplier. If a row exists: roll
   back, `409 SUPPLIER_DELETION_IN_PROGRESS`.

An edit that keeps the supplier takes no lock. The old supplier needs no
check: moving an errand away from a supplier can't create work for it.

`edit_confirmed` is not checked. `pending_edit` holds the complete edited
`Details` as JSON, so Acquire step 3 counts an errand whose
`pending_edit->>'supplierId'` is the fenced supplier as active. A pending
edit onto a supplier therefore either committed before Acquire's lock, and
makes `hasActiveErrands` true (the deletion is refused), or is refused by the
check above. A confirmed edit can never land on a fenced supplier, and
credit-service's adjustment never has to be undone.

### Fence Acquire (B3)

In one `READ COMMITTED` transaction:

1. `SELECT pg_advisory_xact_lock(hashtextextended('supplier:' || $1::text, 0));`
   This waits until every creation holding the shared lock has committed or
   rolled back.
2. `INSERT INTO supplier_fences (operation_id, supplier_id, state) VALUES ($op, $1, 'OPEN') ON CONFLICT (operation_id) DO NOTHING;`
   then read the row. A different `supplier_id` for the same operation:
   roll back, `400 INVALID_ARGUMENT`.
3. Count errands in an active (non-terminal) state whose `supplier_id` is
   the supplier, or whose `pending_edit->>'supplierId'` is the supplier
   (cast to `uuid` before comparing). This statement takes a new snapshot
   after the lock, so it sees every creation and edit that committed while
   Acquire waited.
4. Commit and return `{operationId, supplierId, state, hasActiveErrands}`.

Why there is no gap: a creation (or a supplier-changing edit) either took its
shared lock before Acquire's exclusive lock, in which case Acquire waits for
it and counts its errand, or after, in which case the fence row is committed
and its fence check rejects it. The transactions must be `READ COMMITTED` (the Postgres
default); under `REPEATABLE READ` the count could use a snapshot older than
the lock.

Commit and Release need no advisory lock: they change only the fence row,
`UPDATE … SET state = $new WHERE operation_id = $op AND state = 'OPEN'`, and
then read it to tell "already in that state" (idempotent `200`) from "in the
other terminal state" (`409 INVALID_TRANSITION`).

### Wire format (gate C2)

| Supplier-service call | Order-service route | Result |
| --- | --- | --- |
| `Acquire(operationId, supplierId)` | `POST /internal/supplier-fences/{operationId}/acquire` `{supplierId}` | `200 Fence`; `400 INVALID_ARGUMENT` for another supplier |
| `Inspect(operationId)` | `GET /internal/supplier-fences/{operationId}` | `200 Fence`; `404 FENCE_NOT_FOUND` |
| `Commit(operationId)` | `POST /internal/supplier-fences/{operationId}/commit` | `200 Fence`; `404`; `409` if released |
| `Release(operationId)` | `POST /internal/supplier-fences/{operationId}/release` | `200 Fence`; `404`; `409` if committed |

`Fence.state` on the wire is lower case (`open`, `committed`, `released`),
matching supplier-service's `deletefeature.FenceState`; the
`supplier_fences.state` column stores upper case. A timeout, `5xx` or
network error is ambiguous to supplier-service: it calls Inspect and never
assumes the fence is held or absent. `404` on Inspect means Acquire never
committed. Every Acquire, Commit and Release writes an `admin_audit` row whose
actor is the calling service's `sub`.

## RabbitMQ contract

Shared broker topology (gate C6; the `rabbitmq` container is defined in the
root `compose.yaml`):

| Exchange | Type | Durable | Carries | Declared by |
| --- | --- | --- | --- | --- |
| `foc.events` | topic | yes | `errand.*` events (order-service), `credit.*` results (credit-service), other services' events | every publisher, idempotently |
| `foc.commands` | direct | yes | `credit.*` commands (order-service → credit-service) | every publisher, idempotently |

| Queue | Owner | Binding | Notes |
| --- | --- | --- | --- |
| `credit-service.commands` | credit-service | `foc.commands`: `credit.reserve`, `credit.adjust`, `credit.release`, `credit.transfer` | own DLQ; order-service never declares it |
| `order-service.credit-results` | order-service | `foc.events`: `credit.#` | quorum, `x-delivery-limit` 5, dead-letters to `order-service.credit-results.dlq` |
| `order-events.tap` | order-service (optional) | `foc.events`: `errand.#` | only when `AMQP_DECLARE_TAP` is set; for inspecting events before a notification consumer exists |
| notification queues | notification-service | `foc.events`: `errand.#` or a subset (gate C4) | own DLQ |

Publishing (M2):

- Implemented by `internal/messaging.Publisher`, driven by the standalone
  `cmd/outbox-worker` dispatcher. Publishing is mandatory: an unroutable command
  or event is retried rather than marked `SENT`. Enable the optional tap locally
  if notification-service has not declared an event queue yet.
- Persistent messages (`delivery_mode` 2), published on a confirm-mode
  channel. `Publish` returns only after the broker confirms; a nack, timeout
  or closed channel is retried by the outbox.
- AMQP properties: `message_id` = `messageId`, `type` = the routing key,
  `correlation_id` = `correlationId` when set, `content_type` =
  `application/json`, `timestamp` = `occurredAt`.
- The routing key of an event row is its kind without the `event.` prefix;
  the routing key of a credit row is its kind.

Consuming (every consumer, including order-service's):

- Delivery is at-least-once and may reorder. Deduplicate by `messageId`.
  For `errand.*` events, ignore an event whose `errandVersion` is lower than
  one already applied for that errand. The `withdrawn`/`expired` pair shares
  a version, so key deduplication by `messageId`, not version alone.
- Ack only after the effect is durably recorded. A message that can never be
  processed (unknown type, undecodable body) is rejected without requeue so
  it dead-letters.

### Envelope

Every message body, in both directions, is the `errand.Envelope`:

```json
{
  "messageId": "6f1c2b0e-8d4a-4e1b-9c3f-2a7d5e8b9c10:picked_up:5",
  "correlationId": "results only: the messageId of the command answered",
  "type": "errand.picked_up",
  "occurredAt": "2026-10-14T09:12:30Z",
  "errandId": "6f1c2b0e-8d4a-4e1b-9c3f-2a7d5e8b9c10",
  "errandVersion": 5,
  "data": {}
}
```

| Field | Meaning |
| --- | --- |
| `messageId` | The outbox row's idempotency key for messages order-service sends. Stable across retries, re-sends and replays. |
| `correlationId` | Credit results only. Omitted otherwise. |
| `type` | The routing key. |
| `occurredAt` | When the state change committed (the transition's time), not when it was published. |
| `errandId`, `errandVersion` | The errand and its version after the change. A rejection event carries the version at refusal; a reward-edit request keeps the version unchanged. |
| `data` | `EventData`, `RejectionData`, `ReminderData`, `CreditCommand` or `CreditResult`, by `type`. Omitted fields are absent, never `null`. |

## Errand events

Published on `foc.events` for every transition (F5.1), from the outbox rows
`errand.Next` returns. Recipients are chosen by notification-service (gate
C4); `data` carries enough for that choice without a call back to
order-service.

### Transition events: `EventData`

| Field | Meaning |
| --- | --- |
| `errandId`, `errandVersion` | As in the envelope. |
| `event` | The domain event, e.g. `accept`, `pickup_deadline_passed`. |
| `from`, `to` | States; `from` is omitted for `errand.placed`. |
| `actorType`, `actorId` | Who caused it; `actorId` omitted for `SYSTEM`. |
| `reason` | Free-text reason (dispute, issue report, admin resolution) or a reason code (`INSUFFICIENT_CREDITS`, `RESERVATION_FAILED`, `EDIT_FAILED`). |
| `reviewReason` | Set while the errand is in review. |
| `requesterId` | Always set. |
| `courierId` | The courier involved, including one who has just withdrawn or been withdrawn (F5.1.3). |
| `reward` | The reward after the change. |
| `at` | Transition time. |
| `pendingEdit` | The requested details, on `errand.edit_requested` only. |
| `editDropped` | `true` on `errand.accepted` when the accept discarded a pending edit (F3.4.2). |

The samples below use one errand,
`6f1c2b0e-8d4a-4e1b-9c3f-2a7d5e8b9c10` (written `E` in keys for brevity;
real keys contain the full UUID), requester `auth0|req1` and courier
`auth0|cou1`.

`errand.placed`: submitted, reservation requested.

```json
{"messageId": "E:placed:1", "type": "errand.placed", "occurredAt": "2026-10-14T09:00:00Z",
 "errandId": "E", "errandVersion": 1,
 "data": {"errandId": "E", "errandVersion": 1, "event": "submit", "to": "PENDING_RESERVATION",
          "actorType": "REQUESTER", "actorId": "auth0|req1", "requesterId": "auth0|req1",
          "reward": 5, "at": "2026-10-14T09:00:00Z"}}
```

`errand.opened`: credits reserved; visible to couriers.

```json
{"messageId": "E:opened:2", "type": "errand.opened", "occurredAt": "2026-10-14T09:00:01Z",
 "errandId": "E", "errandVersion": 2,
 "data": {"errandId": "E", "errandVersion": 2, "event": "reservation_confirmed",
          "from": "PENDING_RESERVATION", "to": "OPEN", "actorType": "SYSTEM",
          "requesterId": "auth0|req1", "reward": 5, "at": "2026-10-14T09:00:01Z"}}
```

`errand.rejected`: reservation refused (`reason` is the credit code) or
compensated after a timeout (`RESERVATION_FAILED`).

```json
{"messageId": "E:rejected:2", "type": "errand.rejected", "occurredAt": "2026-10-14T09:00:01Z",
 "errandId": "E", "errandVersion": 2,
 "data": {"errandId": "E", "errandVersion": 2, "event": "reservation_refused",
          "from": "PENDING_RESERVATION", "to": "REJECTED", "actorType": "SYSTEM",
          "reason": "INSUFFICIENT_CREDITS", "requesterId": "auth0|req1", "reward": 5,
          "at": "2026-10-14T09:00:01Z"}}
```

`errand.edit_requested`: a reward change is waiting for credit-service; the
version is unchanged.

```json
{"messageId": "E:edit_requested:2", "type": "errand.edit_requested", "occurredAt": "2026-10-14T09:05:00Z",
 "errandId": "E", "errandVersion": 2,
 "data": {"errandId": "E", "errandVersion": 2, "event": "edit", "from": "OPEN", "to": "OPEN",
          "actorType": "REQUESTER", "actorId": "auth0|req1", "requesterId": "auth0|req1",
          "reward": 5, "at": "2026-10-14T09:05:00Z",
          "pendingEdit": {"supplierId": "1d0b…", "supplierVersionId": "9a3e…",
                          "deliveryBuildingId": "c41f…", "deliveryBuildingSnapshot": {"id": "c41f…", "name": "Com 2"},
                          "deliveryDetail": "Level 2 lobby", "items": [{"description": "Iced latte", "quantity": 1}],
                          "notes": "", "reward": 8, "acceptanceDeadline": "2026-10-14T10:00:00Z"}}}
```

`errand.edited`: an edit was applied, at once (reward unchanged, actor
`REQUESTER`) or after `credit.adjusted` (event `edit_confirmed`, actor
`SYSTEM`).

```json
{"messageId": "E:edited:3", "type": "errand.edited", "occurredAt": "2026-10-14T09:05:02Z",
 "errandId": "E", "errandVersion": 3,
 "data": {"errandId": "E", "errandVersion": 3, "event": "edit_confirmed", "from": "OPEN", "to": "OPEN",
          "actorType": "SYSTEM", "requesterId": "auth0|req1", "reward": 8,
          "at": "2026-10-14T09:05:02Z"}}
```

`errand.edit_rejected`: the pending edit was dropped (`INSUFFICIENT_CREDITS`,
`RESERVATION_NOT_ACTIVE`, or `EDIT_FAILED` after a timeout).

```json
{"messageId": "E:edit_rejected:3", "type": "errand.edit_rejected", "occurredAt": "2026-10-14T09:05:02Z",
 "errandId": "E", "errandVersion": 3,
 "data": {"errandId": "E", "errandVersion": 3, "event": "edit_refused", "from": "OPEN", "to": "OPEN",
          "actorType": "SYSTEM", "reason": "INSUFFICIENT_CREDITS", "requesterId": "auth0|req1",
          "reward": 5, "at": "2026-10-14T09:05:02Z"}}
```

`errand.accepted`:

```json
{"messageId": "E:accepted:4", "type": "errand.accepted", "occurredAt": "2026-10-14T09:10:00Z",
 "errandId": "E", "errandVersion": 4,
 "data": {"errandId": "E", "errandVersion": 4, "event": "accept", "from": "OPEN", "to": "ASSIGNED",
          "actorType": "COURIER", "actorId": "auth0|cou1", "requesterId": "auth0|req1",
          "courierId": "auth0|cou1", "reward": 8, "at": "2026-10-14T09:10:00Z"}}
```

`errand.cancelled`:

```json
{"messageId": "E:cancelled:4", "type": "errand.cancelled", "occurredAt": "2026-10-14T09:10:00Z",
 "errandId": "E", "errandVersion": 4,
 "data": {"errandId": "E", "errandVersion": 4, "event": "cancel", "from": "OPEN", "to": "CANCELLED",
          "actorType": "REQUESTER", "actorId": "auth0|req1", "requesterId": "auth0|req1",
          "reward": 8, "at": "2026-10-14T09:10:00Z"}}
```

`errand.withdrawn`: voluntary (`withdraw`, actor `COURIER`) or forced by the
pickup deadline (`pickup_deadline_passed`, actor `SYSTEM`). `courierId` is
the courier who left.

```json
{"messageId": "E:withdrawn:5", "type": "errand.withdrawn", "occurredAt": "2026-10-14T11:10:00Z",
 "errandId": "E", "errandVersion": 5,
 "data": {"errandId": "E", "errandVersion": 5, "event": "pickup_deadline_passed", "from": "ASSIGNED",
          "to": "OPEN", "actorType": "SYSTEM", "requesterId": "auth0|req1",
          "courierId": "auth0|cou1", "reward": 8, "at": "2026-10-14T11:10:00Z"}}
```

`errand.expired`: the acceptance deadline passed while `OPEN`, or a
withdrawal happened after it. In the second case it follows
`errand.withdrawn` with the same version and `to: EXPIRED` in both.

```json
{"messageId": "E:expired:5", "type": "errand.expired", "occurredAt": "2026-10-14T10:00:00Z",
 "errandId": "E", "errandVersion": 5,
 "data": {"errandId": "E", "errandVersion": 5, "event": "acceptance_deadline_passed", "from": "OPEN",
          "to": "EXPIRED", "actorType": "SYSTEM", "requesterId": "auth0|req1", "reward": 8,
          "at": "2026-10-14T10:00:00Z"}}
```

`errand.picked_up`:

```json
{"messageId": "E:picked_up:5", "type": "errand.picked_up", "occurredAt": "2026-10-14T09:30:00Z",
 "errandId": "E", "errandVersion": 5,
 "data": {"errandId": "E", "errandVersion": 5, "event": "pick_up", "from": "ASSIGNED", "to": "PICKED_UP",
          "actorType": "COURIER", "actorId": "auth0|cou1", "requesterId": "auth0|req1",
          "courierId": "auth0|cou1", "reward": 8, "at": "2026-10-14T09:30:00Z"}}
```

`errand.delivered`:

```json
{"messageId": "E:delivered:6", "type": "errand.delivered", "occurredAt": "2026-10-14T09:45:00Z",
 "errandId": "E", "errandVersion": 6,
 "data": {"errandId": "E", "errandVersion": 6, "event": "deliver", "from": "PICKED_UP", "to": "DELIVERED",
          "actorType": "COURIER", "actorId": "auth0|cou1", "requesterId": "auth0|req1",
          "courierId": "auth0|cou1", "reward": 8, "at": "2026-10-14T09:45:00Z"}}
```

`errand.review_required`: courier issue, requester dispute or delivery
overdue; `reason` is the reporter's text, omitted for `DELIVERY_OVERDUE`.

```json
{"messageId": "E:review_required:7", "type": "errand.review_required", "occurredAt": "2026-10-14T09:50:00Z",
 "errandId": "E", "errandVersion": 7,
 "data": {"errandId": "E", "errandVersion": 7, "event": "dispute", "from": "DELIVERED",
          "to": "REVIEW_REQUIRED", "actorType": "REQUESTER", "actorId": "auth0|req1",
          "reason": "Wrong drink delivered", "reviewReason": "REQUESTER_DISPUTE",
          "requesterId": "auth0|req1", "courierId": "auth0|cou1", "reward": 8,
          "at": "2026-10-14T09:50:00Z"}}
```

`errand.completed`: requester confirmed (`confirm`), verification lapsed
(`verification_lapsed`) or admin resolved for the courier (`admin_complete`).

```json
{"messageId": "E:completed:7", "type": "errand.completed", "occurredAt": "2026-10-14T10:15:00Z",
 "errandId": "E", "errandVersion": 7,
 "data": {"errandId": "E", "errandVersion": 7, "event": "verification_lapsed", "from": "DELIVERED",
          "to": "COMPLETED", "actorType": "SYSTEM", "requesterId": "auth0|req1",
          "courierId": "auth0|cou1", "reward": 8, "at": "2026-10-14T10:15:00Z"}}
```

`errand.refunded`: admin resolved for the requester.

```json
{"messageId": "E:refunded:8", "type": "errand.refunded", "occurredAt": "2026-10-14T12:00:00Z",
 "errandId": "E", "errandVersion": 8,
 "data": {"errandId": "E", "errandVersion": 8, "event": "admin_refund", "from": "REVIEW_REQUIRED",
          "to": "REFUNDED", "actorType": "ADMIN", "actorId": "auth0|adm1",
          "reason": "Courier confirmed the order was never collected", "reviewReason": "REQUESTER_DISPUTE",
          "requesterId": "auth0|req1", "courierId": "auth0|cou1", "reward": 8,
          "at": "2026-10-14T12:00:00Z"}}
```

### Rejection events: `RejectionData`

A request refused with `409` by a state check also notifies the person who
made it. These are not transitions: the errand is unchanged and
`errandVersion`/`status` are its state at refusal. `actorId` is the person to
notify; `code` is the HTTP error code returned.

`errand.accept_rejected` (A4; `ERRAND_CHANGED` or `ERRAND_UNAVAILABLE` only,
never the `403` eligibility refusals):

```json
{"messageId": "E:accept-rejected:req-7f3a", "type": "errand.accept_rejected", "occurredAt": "2026-10-14T09:10:01Z",
 "errandId": "E", "errandVersion": 4,
 "data": {"errandId": "E", "errandVersion": 4, "status": "ASSIGNED", "action": "accept",
          "code": "ERRAND_UNAVAILABLE", "actorType": "COURIER", "actorId": "auth0|cou2",
          "at": "2026-10-14T09:10:01Z"}}
```

`errand.cancel_rejected` (A5):

```json
{"messageId": "E:cancel-rejected:req-91bc", "type": "errand.cancel_rejected", "occurredAt": "2026-10-14T09:10:02Z",
 "errandId": "E", "errandVersion": 4,
 "data": {"errandId": "E", "errandVersion": 4, "status": "ASSIGNED", "action": "cancel",
          "code": "ERRAND_UNAVAILABLE", "actorType": "REQUESTER", "actorId": "auth0|req1",
          "at": "2026-10-14T09:10:02Z"}}
```

`errand.pickup_rejected` (A6):

```json
{"messageId": "E:pickup-rejected:req-4d20", "type": "errand.pickup_rejected", "occurredAt": "2026-10-14T11:10:05Z",
 "errandId": "E", "errandVersion": 5,
 "data": {"errandId": "E", "errandVersion": 5, "status": "OPEN", "action": "pickup",
          "code": "ERRAND_UNAVAILABLE", "actorType": "COURIER", "actorId": "auth0|cou1",
          "at": "2026-10-14T11:10:05Z"}}
```

### Reminder events: `ReminderData`

Inserted by B's scheduler (B4b); they change no state. `deadline` is the
deadline the reminder is about.

| Field | Meaning |
| --- | --- |
| `errandId`, `errandVersion`, `status` | The errand when the reminder was raised. |
| `requesterId`, `courierId` | Both parties. |
| `deadline` | `verificationDeadline` or `pickupDeadline`. |
| `at` | When the reminder was raised. |

`errand.verification_reminder` (F3.10.3; for the requester):

```json
{"messageId": "E:verify-reminder:6", "type": "errand.verification_reminder", "occurredAt": "2026-10-14T09:50:00Z",
 "errandId": "E", "errandVersion": 6,
 "data": {"errandId": "E", "errandVersion": 6, "status": "DELIVERED", "requesterId": "auth0|req1",
          "courierId": "auth0|cou1", "deadline": "2026-10-14T10:15:00Z", "at": "2026-10-14T09:50:00Z"}}
```

`errand.pickup_deadline_approaching` (for the courier):

```json
{"messageId": "E:pickup-reminder:4", "type": "errand.pickup_deadline_approaching", "occurredAt": "2026-10-14T11:00:00Z",
 "errandId": "E", "errandVersion": 4,
 "data": {"errandId": "E", "errandVersion": 4, "status": "ASSIGNED", "requesterId": "auth0|req1",
          "courierId": "auth0|cou1", "deadline": "2026-10-14T11:10:00Z", "at": "2026-10-14T11:00:00Z"}}
```

## Credit message contract (proposed, gate C1)

Copied from `references/commit-plan-b.md` "Proposed credit message contract".
Credit-service has no code yet; C builds M3 and M4 against this using
`testfakes.FakeCreditService`, and agrees it with the credit-service owner
before M4 merges. Changes are made here and in `internal/errand/ports.go` in
one joint contract commit.

**Commands** (order-service → credit-service): exchange `foc.commands`
(durable, `direct`), routing key = the command name, queue
`credit-service.commands` (owned and declared by credit-service, with its own
dead-letter queue). Persistent messages, published with confirms.
`messageId` = the outbox row's idempotency key. `data` is the row's
`errand.CreditCommand`, so every command carries `errandId` and
`errandVersion`; the table lists the other fields each command uses.

**Results** (credit-service → order-service): exchange `foc.events`, routing
key = the result name. `correlationId` = the command's `messageId`;
`errandId` and `errandVersion` are copied from the command. Proposed
`messageId`: `{correlationId}:{result}`, so a re-published result is a
duplicate consumers drop. Order-service consumes them from
`order-service.credit-results` (bound to `credit.#`, dead-letter queue
`order-service.credit-results.dlq`).

| Command | `data` | Success result | Refusal result |
| --- | --- | --- | --- |
| `credit.reserve` | `{requesterId, amount}` | `credit.reserved` `{amount}` | `credit.reservation_refused` `{code: INSUFFICIENT_CREDITS \| ACCOUNT_NOT_FOUND \| RESERVATION_RELEASED}` |
| `credit.adjust` | `{requesterId, newAmount, seq}` | `credit.adjusted` `{amount, seq}` | `credit.adjust_refused` `{code: INSUFFICIENT_CREDITS \| RESERVATION_NOT_ACTIVE, seq}` |
| `credit.release` | `{requesterId}` | `credit.released` (also when already released) | `credit.operation_failed` `{code: RESERVATION_ALREADY_TRANSFERRED}` |
| `credit.transfer` | `{requesterId, courierId, amount}` | `credit.transferred` (also when already transferred) | `credit.operation_failed` `{code: RESERVATION_ALREADY_RELEASED}` |

Rules credit-service must follow:

- **Idempotent by `messageId`.** A redelivered command re-publishes the
  original result and changes no balance.
- **Release tombstone.** A release for an unknown errand records a tombstone,
  so a reserve processed later is refused with `RESERVATION_RELEASED`. This
  covers "the reconciler rejected the errand while the reserve was still
  queued".
- **Adjust ordering.** `newAmount` is absolute. An adjust whose `seq` is not
  newer than the last applied one for that reservation is acknowledged with
  the current amount and changes nothing. This makes the reverting adjust
  (sent when an accept or timeout drops a pending edit) safe even if it
  overtakes the original.
- **`credit.operation_failed`** means a contract bug or a state that should be
  impossible. Order-service marks the row `FAILED` for an admin and doesn't
  retry it.

**`seq` values.** `commit-plan.md` describes `seq` as "the errand version".
The S0c implementation refines this, because a reward-edit request keeps the
version and a revert at version `w` can be followed by an edit at the same
version: an edit made against version `v` sends `seq = 2v`
(`errand.AdjustSeq`); a revert by the transition to version `w` sends
`seq = 2w − 1` (`errand.RevertSeq`). Seqs strictly increase per errand, which
is all credit-service relies on.

How order-service handles each result (M4): `credit.reserved` →
`PENDING_RESERVATION → OPEN`; `credit.reservation_refused` → `REJECTED` with
the code as reason; `credit.adjusted` / `credit.adjust_refused` → apply or drop
the pending edit if `pending_edit_key` equals `correlationId`, otherwise
ignore; `credit.released` / `credit.transferred` → row `CONFIRMED`;
`credit.operation_failed` → row `FAILED`. Every handler is idempotent by
state, so a redelivered result changes nothing.

Samples, for an errand created at version 1 and edited at version 2:

```json
{"messageId": "E:reserve", "type": "credit.reserve", "occurredAt": "2026-10-14T09:00:00Z",
 "errandId": "E", "errandVersion": 1,
 "data": {"errandId": "E", "errandVersion": 1, "requesterId": "auth0|req1", "amount": 5}}
```

```json
{"messageId": "E:reserve:credit.reserved", "correlationId": "E:reserve", "type": "credit.reserved",
 "occurredAt": "2026-10-14T09:00:01Z", "errandId": "E", "errandVersion": 1,
 "data": {"amount": 5}}
```

```json
{"messageId": "E:adjust:2", "type": "credit.adjust", "occurredAt": "2026-10-14T09:05:00Z",
 "errandId": "E", "errandVersion": 2,
 "data": {"errandId": "E", "errandVersion": 2, "requesterId": "auth0|req1", "newAmount": 8, "seq": 4}}
```

```json
{"messageId": "E:adjust:2:credit.adjust_refused", "correlationId": "E:adjust:2", "type": "credit.adjust_refused",
 "occurredAt": "2026-10-14T09:05:01Z", "errandId": "E", "errandVersion": 2,
 "data": {"code": "INSUFFICIENT_CREDITS", "seq": 4}}
```

```json
{"messageId": "E:transfer", "type": "credit.transfer", "occurredAt": "2026-10-14T10:15:00Z",
 "errandId": "E", "errandVersion": 7,
 "data": {"errandId": "E", "errandVersion": 7, "requesterId": "auth0|req1",
          "courierId": "auth0|cou1", "amount": 8}}
```

```json
{"messageId": "E:release", "type": "credit.release", "occurredAt": "2026-10-14T09:10:00Z",
 "errandId": "E", "errandVersion": 4,
 "data": {"errandId": "E", "errandVersion": 4, "requesterId": "auth0|req1"}}
```

## Cache contract

Redis (`order-redis`) is optional and private to order-service. Postgres is
always the source of truth.

- **Nothing that is checked before a write reads from a cache.** Accept,
  edit, cancel and every other transition read the errand from Postgres and
  rely on the version compare-and-swap. `SupplierGate.ResolveForCreate` and
  fence state are never cached.
- **Any cache error is a miss** (connection refused, ~50 ms timeout, open
  breaker, undecodable value), never an error to the caller. An empty
  `REDIS_URL` selects a cache that always misses.
- Every key starts with `order:`.

| Key | Value | TTL | Owner |
| --- | --- | --- | --- |
| `order:open-errands:v1` | JSON array of `errand.OpenErrandView`: every open errand with `acceptance_deadline > now()` | set by A3, 1–2 s | B2b adapter, A3 reads and fills it |
| `order:supplier:{supplierId}:v{versionId}` | supplier-service `SupplierVersion` | none (versions are immutable) | B2c |
| `order:buildings` | supplier-service building list | 5 min | B2c |

The open-errand value holds only the redacted projection: no requester
identity, no notes and no contact person (NFR3.6.4, NFR3.6.3), enforced by the type and its test
in `internal/errand`. Courier coordinates are never cached; filtering,
distance ordering and pagination run per request on the cached set. On a miss
A3 loads the set through a `singleflight` group, so concurrent misses cause
one query. The `:v1` suffix changes whenever `OpenErrandView` changes shape.

## Internal routes

All internal routes need a service token with `orders:internal`
(`serviceauth.RequireService`). Each calling service has its own Auth0 M2M
client, so `sub` (`<client-id>@clients`) identifies the caller in logs and
audit rows.

| Caller | Route | Purpose |
| --- | --- | --- |
| supplier-service | `POST /internal/supplier-fences/{operationId}/acquire` | Block creation and check for active errands (F2.4.2) |
| supplier-service | `GET /internal/supplier-fences/{operationId}` | Resolve an ambiguous acquire or commit |
| supplier-service | `POST /internal/supplier-fences/{operationId}/commit` | Make the block permanent after the soft delete |
| supplier-service | `POST /internal/supplier-fences/{operationId}/release` | Lift the block when deletion is rejected or fails |
| user-service | `GET /internal/users/{userId}/active-errands` | Account-deletion check (F1.2.2, F1.2.3; gate C5) |
| none (unauthenticated) | `GET /readyz` | Readiness (NFR4.1.2) |

`orders:internal` is one permission for both callers. If NFR3.5.3 review asks
for narrower grants, split it into `orders:fence` and `orders:user-activity`
in a joint contract commit; the routes do not change.

Order-service calls out with its own M2M client to supplier-service only
(`GET /suppliers/{id}`, `GET /supplier-versions/{versionId}`,
`GET /buildings`, needing `suppliers:read`).
