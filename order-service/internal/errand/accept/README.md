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

## Request flow

1. Authenticate: a user token with `errands:use`. The courier is the token's
   `sub`.
2. Parse `errandId` (UUID) and the `VersionedAction` body
   (`{"expectedVersion": n}`, nothing else).
3. `Store.Get` the errand from Postgres (never the cache).
4. **Retry of a committed accept.** If the errand is `ASSIGNED` to the caller
   at `expectedVersion + 1`, answer `200` with the courier view and stop.
5. **Eligibility input.**
   - `Settings.Get` gives the deadlines and the restriction rule.
   - `Store.CourierHistory(errand, courier, now − window)` gives
     `WithdrewFromErrand`, which becomes `PreviouslyWithdrew`.
   - `WithdrawalsSince ≥ count` becomes `Restricted`.
6. `errand.Next(e, Accept, courier, input, now)`.
7. If `e.Version ≠ expectedVersion`, the answer is `409 ERRAND_CHANGED`.
8. `Store.Apply(t)`.
9. Answer `200` with `CourierErrandView`, which includes the contact person
   now that the errand is `ASSIGNED`.

## Responses

| Status | Code | When | Announced |
| --- | --- | --- | --- |
| 200 | | Accepted, or a retry of an accept that committed | |
| 400 | `INVALID_ARGUMENT` | Malformed `errandId`; body empty or not a single JSON object | |
| 400 | `VALIDATION_FAILED` | `expectedVersion` missing or not a positive integer; any other member | |
| 401 | `UNAUTHENTICATED` | No valid token | |
| 403 | `FORBIDDEN` | No `errands:use`, or a service token | |
| 403 | `NOT_ELIGIBLE` | The caller is the requester, or withdrew from this errand (voluntarily or forced) | no |
| 403 | `COURIER_RESTRICTED` | The caller is under a withdrawal restriction | no |
| 404 | `ERRAND_NOT_FOUND` | No such errand | |
| 409 | `ERRAND_CHANGED` | `expectedVersion` is stale | **yes** |
| 409 | `ERRAND_UNAVAILABLE` | Accepted by someone else, cancelled, expired, past the acceptance deadline, or lost the compare-and-swap | **yes** |
| 409 | `INVALID_TRANSITION` | Not open yet (`PENDING_RESERVATION`) | no |
| 503 | `DEPENDENCY_UNAVAILABLE` | Supplier-service is down while building the response (the accept has committed) | |
| 500 | `INTERNAL` | Settings or history unreadable, or anything else | |

"Announced" means `Store.RecordRejection` writes an
`event.errand.accept_rejected` outbox row so the courier is notified
(`docs/contracts.md`, "Rejection events").

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
over both, because Next checks the actor first.

### Retrying a committed accept answers 200

The accept commits before the response is built, and building it calls
supplier-service for the pickup location. If that call fails, the client gets
`503` for an accept that succeeded. Without step 4, its retry would find the
errand `ASSIGNED` and get `409 ERRAND_UNAVAILABLE`, and would also notify the
courier that their own accept was rejected.

Step 4 recognises the retry safely: only an accept moves an errand from `OPEN`
to `ASSIGNED`, and it adds exactly one to the version, so `ASSIGNED` to the
caller at `expectedVersion + 1` can only be the result of this accept.

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
