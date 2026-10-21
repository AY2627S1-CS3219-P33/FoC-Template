# Courier actions (A6)

`courier` serves the assigned courier's four errand actions (commit A6,
`feat(order-courier): withdraw, pick up, deliver and report issues`):

| Endpoint | Transition | Credit effect |
| --- | --- | --- |
| `POST /errands/{errandId}/withdraw` | `ASSIGNED → OPEN` before the acceptance deadline, `ASSIGNED → EXPIRED` after it (F3.6.1) | none, or `credit.release` when it expires (F4.3.3) |
| `POST /errands/{errandId}/pickup` | `ASSIGNED → PICKED_UP` (F3.8.1) | none |
| `POST /errands/{errandId}/deliver` | `PICKED_UP → DELIVERED`, verification deadline set (F3.8.2) | none; credits move on confirm |
| `POST /errands/{errandId}/report-issue` | `PICKED_UP → REVIEW_REQUIRED` with `COURIER_ISSUE` (F3.6.2) | none; the reservation stays held |

As in A4 and A5, `errand.Next` decides every transition and its outbox rows,
and `Store.Apply` persists it with a compare-and-swap. A withdrawal past the
acceptance deadline emits `errand.withdrawn` and `errand.expired`. Every
withdrawal is an `errand_transitions` row with the courier's ID, so it counts
towards the restriction (F3.6.4) and bars the courier from accepting the
errand again (F3.6.3). The handler adds nothing for this.

## Requests

```http
POST /errands/6f1c2a9e-0b7d-4c3e-9a51-2d8e4f7b1c90/pickup
Authorization: Bearer <user token with errands:use>
Content-Type: application/json

{ "expectedVersion": 3 }
```

Withdraw, pickup and deliver take `VersionedAction`. Report-issue takes
`ReasonedAction`: `expectedVersion` plus a required, non-blank `reason` of at
most 500 characters, which becomes the review's note. All four answer `200`
with `CourierErrandView`. After a withdrawal the errand is `OPEN` or
`EXPIRED`, so the contact person is no longer shown.

Deliver reads the settings for the verification deadline. The other actions
do not read them.

## Who may call

The contract's relationship is "assigned courier" (NFR3.3.2). The handler
sorts callers into three groups before it asks `errand.Next`:

| Caller | Answer |
| --- | --- |
| The errand's `courierId` | Carry on to the retry check and `errand.Next` |
| A **former** courier: `Store.CourierHistory` says they withdrew, or the pickup deadline withdrew them | `409 ERRAND_UNAVAILABLE` ("you are no longer this errand's courier"), except a withdrawal's own retry (below) |
| Anyone else, including the requester | `403 FORBIDDEN`, whatever the state |

A former courier needs a group of their own because the pickup-deadline job
clears `courier_id`. Without it, the late pickup that A6 has to announce
would just be a `403`. A courier who withdrew can never accept the errand
again (F3.6.3), so "withdrew from it" is exactly "was its courier and is not
now". Strangers still learn nothing about the errand and cannot add rejection
events to the outbox.

## Retries answer 200

| Action | Recognised when the errand is |
| --- | --- |
| withdraw | `OPEN` or `EXPIRED` at `expectedVersion + 1`, and the caller is a former courier |
| pickup | `PICKED_UP` at `expectedVersion + 1` with the caller as courier |
| deliver | `DELIVERED` at `expectedVersion + 1` with the caller as courier |
| report issue | `REVIEW_REQUIRED` with `COURIER_ISSUE` at `expectedVersion + 1` with the caller as courier |

## Order of checks

1. token and scope (`401`, `403`);
2. `errandId` (`400 INVALID_ARGUMENT`);
3. body (`400`);
4. errand lookup (`404 ERRAND_NOT_FOUND`), then the caller (table above);
5. the retry check;
6. settings, for deliver only;
7. `errand.Next`: state (`409 ERRAND_UNAVAILABLE` / `INVALID_TRANSITION`),
   then the reason (`400 VALIDATION_FAILED`);
8. `expectedVersion` (`409 ERRAND_CHANGED`);
9. supplier-service, for the response, read before the write;
10. compare-and-swap in `Store.Apply`.

## Rejection events

Only **pickup** is announced. A `409 ERRAND_CHANGED`, `ERRAND_UNAVAILABLE` or
`INVALID_TRANSITION` refusal, including a lost compare-and-swap against the
pickup-deadline job, also writes an `event.errand.pickup_rejected` row
through `Store.RecordRejection` (`docs/contracts.md`, "Rejection events"). The
request ID comes from `errandhttp.RejectionRequestID`, as in A4 and A5. This
is best effort: if it fails, the error is logged and the `409` is still
returned. `403` refusals are never announced.

## Open concerns

- **A withdrawal's retry is recognised by its shape.** A forced withdrawal by
  the pickup deadline leaves the same shape (`OPEN`/`EXPIRED` one version
  later). If one lands at the same moment as the courier's own withdraw, the
  courier gets `200` for a withdrawal the system made. The outcome they asked
  for (off the errand, counted towards the restriction) holds either way.
  Telling the two apart would need a new `Store` read of the transition.
- **Not wired into the service.** `courier.NewHandler` is registered in the
  integration commits (I1–I3), like A4 and A5.
- **Race detector not run** (no cgo on the development machine). The handler
  holds no shared mutable state.
