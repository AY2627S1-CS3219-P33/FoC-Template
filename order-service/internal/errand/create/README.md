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

## Request flow

1. Authenticate: a user token with `errands:use` (`auth.Middleware.RequireUse`).
   The requester is the token's `sub`; identity in the body is refused
   (NFR3.3.1).
2. Read the `Idempotency-Key` header. It must be a UUID and is lower-cased.
3. **Replay check.** `Store.Get` on the errand ID derived from (requester,
   key) (see [Derived errand IDs](#derived-errand-ids)). If it exists, answer
   `200` with the errand in its current state. Nothing else runs.
4. Read and validate the body (`input.go`). Every invalid field is reported in
   one `400 VALIDATION_FAILED` (NFR2.2.1). The acceptance deadline defaults to
   one hour and must be 10 minutes to 24 hours away, both inclusive (F3.1.3).
5. Check the delivery building against `SupplierGate.ListBuildings`: it must
   exist and not be retired. Its record is stored as the snapshot (F3.1.1).
6. `SupplierGate.ResolveForCreate`: the supplier's current version, live and
   never cached (F2.5.2).
7. `errand.Next(Submit)` builds the transition and its outbox rows.
8. `Store.Create` checks the deletion fence and inserts everything in one
   transaction.
9. Answer `202` with a `Location` header, or `200` when `Create` reports that a
   concurrent request with the same key committed first.

## Responses

| Status | Code | When |
| --- | --- | --- |
| 202 | | Created in `PENDING_RESERVATION` |
| 200 | | Replay of an earlier request with the same key |
| 400 | `INVALID_ARGUMENT` | Missing or non-UUID `Idempotency-Key`; body empty, not a single JSON object, or over 64 KiB |
| 400 | `VALIDATION_FAILED` | Invalid body fields, unknown fields, or an unknown or retired delivery building |
| 401 | `UNAUTHENTICATED` | No valid token |
| 403 | `FORBIDDEN` | No `errands:use`, or a service token |
| 409 | `SUPPLIER_DELETION_IN_PROGRESS` | The supplier has an open or committed fence |
| 422 | `SUPPLIER_UNAVAILABLE` | The supplier is missing, deleted or closed |
| 503 | `DEPENDENCY_UNAVAILABLE` | Supplier-service is unreachable |
| 500 | `INTERNAL` | Anything else; logged with the errand ID only |

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
