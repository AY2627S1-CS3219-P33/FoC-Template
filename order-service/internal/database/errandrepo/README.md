# Errand store (A1)

`errandrepo` is the PostgreSQL implementation of [`errand.Store`](../../errand/ports.go)
(commit A1, `feat(order-db): implement errand store`). It is the only writer of
`errands` and `errand_transitions`. It also writes the `outbox` and
`errand_reviews` rows a transition emits, in the same transaction as the errand
row (F4.6.2, F4.6.3, NFR7.4).

The store makes no lifecycle decisions. `errand.Next` decides every transition,
and the store persists it, checking only the compare-and-swap and the supplier
fence. The in-memory [`testfakes.FakeStore`](../../testfakes/store.go) has the
same contract, so code tested against the fake behaves the same on Postgres.

```
errand (domain: Next, the Store port)
   ▼
errandrepo  (this package: transactions, error codes)   ← hand-written
   ▼
errandgen   (sqlc output, one Go function per query)     ← generated, git-ignored
   ▼
../../../queries/errands.sql                             ← hand-written SQL
```

## Usage

```go
pool, err := pgxpool.New(ctx, databaseURL)
store := errandrepo.New(pool) // satisfies errand.Store
```

`New` accepts anything with `Query`/`Exec`/`QueryRow` and `BeginTx`: a
`*pgxpool.Pool` in the service, or a single `*pgx.Conn` in tests. Every write
runs in its own `READ COMMITTED` transaction. The fence protocol in
[docs/contracts.md](../../../docs/contracts.md#fence-and-creation-locking)
requires that isolation level.

## Methods

| Method | Transaction | Notes |
| --- | --- | --- |
| `Create` | lock supplier (shared) → fence check → insert errand, transition, outbox rows | A replay of `(requesterId, createIdempotencyKey)` returns the stored errand with `created = false` before any fence check. A concurrent replay is caught by `ON CONFLICT … DO NOTHING` and re-read. |
| `Apply` | `SELECT … FOR UPDATE` → compare-and-swap → fence check (supplier-changing edits only) → `UPDATE` → transition, review and outbox rows | The new row is computed with `t.ApplyTo(current)`, the same function the fake uses. The `UPDATE` repeats the compare-and-swap in its `WHERE` as a backstop. |
| `RecordRejection` | read the errand's status and version → insert one rejection row | `ON CONFLICT DO NOTHING`, so a retried HTTP request notifies once. |
| `ClaimDue` | `SELECT … FOR UPDATE SKIP LOCKED` (one query per `DueKind`) → `decide` each errand → apply every chosen transition | The whole batch is one transaction. An error from `decide` or from any apply rolls everything back and returns `applied = 0`. |
| `Get` | none | |
| `ListOpen` | none | `OPEN` and deadline after `q.Now`, ordered by `(acceptance_deadline, id)`. |
| `ListRequested` | none | Active errands of the requester, newest `created_at` first. |
| `ListDelivering` | none | Active errands of the courier, newest `updated_at` first. |
| `ListHistory` | none | Terminal errands where the user is requester or courier, newest `updated_at` first. |
| `CourierHistory` | none | Counts `withdraw` and `pickup_deadline_passed` rows by `errand_transitions.courier_id`, so forced withdrawals are attributed to the courier. |

### Rows written with a transition

For `Create`, `Apply` and every transition `ClaimDue` applies:

- **`errand_transitions`:** one row. `courier_id` is the courier before the
  transition, or after it when there was none (an accept). `errand_version` is
  the version after the transition. `at` is `t.At`.
- **`errand_reviews`:**
  - Into `REVIEW_REQUIRED`: insert an open review. `reporter_id` is NULL for
    `SYSTEM`, and `note` is NULL when the reason is blank.
  - Out of `REVIEW_REQUIRED`: resolve the open review. If there is none, that
    is a bug: the transaction rolls back with a plain error.
- **`outbox`:** every row in `t.Outbox`, with its payload JSON-encoded. A
  duplicate idempotency key aborts the transaction with a plain error, because
  `Next` never produces one.

### Timestamps

`created_at`, `updated_at`, transition `at` and review times are taken from
`t.At`, not from the database clock. The stored errand therefore matches what
`t.ApplyTo` returns. The one difference is that Postgres keeps microseconds,
so nanoseconds are truncated.

## Errors

Errors a client should see are `*apperror.Error`. Anything else (a lost
connection, a constraint violation, a duplicate outbox key) is a plain error
that callers treat as `INTERNAL`.

| Code | When |
| --- | --- |
| `ERRAND_NOT_FOUND` | No errand has the ID. A malformed (non-UUID) ID is also "not found". |
| `ERRAND_UNAVAILABLE` | The errand's status is no longer `t.From` (accepted, cancelled, expired… in the meantime, F3.3.7). |
| `ERRAND_CHANGED` | The status matches but the version or pending edit key does not. The caller's view is stale. |
| `SUPPLIER_DELETION_IN_PROGRESS` | `Create`, or an edit moving the errand to another supplier, hit an `OPEN` or `COMMITTED` fence. |
| `INVALID_ARGUMENT` | A Submit passed to `Apply`, a non-Submit to `Create`, a missing create key, a malformed supplier/building/version UUID, an unknown rejection action or due kind, a list limit outside `1..MaxListLimit`, or a malformed cursor. |

Lenient cases:

- A `ListOpen` supplier filter that is not a UUID returns an empty page.
- A `CourierHistory` errand ID that is not a UUID reports no withdrawal from it.

## Supplier fence

The store implements order-service's side of the deletion fence
([docs/contracts.md](../../../docs/contracts.md#fence-and-creation-locking)):

1. Take `pg_advisory_xact_lock_shared(hashtextextended('supplier:' || id, 0))`.
   This is the same key B's Fence Acquire locks exclusively.
2. Refuse if `supplier_fences` has an `OPEN` or `COMMITTED` row for the supplier.

`Create` does this for the new errand's supplier. `Apply` does it only for an
`edit` whose new details (applied or pending) name a different supplier.
`edit_confirmed` is not checked, because Fence Acquire already counts pending
edits.

## Queries

[`queries/errands.sql`](../../../queries/errands.sql) holds one statement per
query. Transactions and decisions stay in Go.

| Group | Queries |
| --- | --- |
| Fence | `LockSupplierShared`, `SupplierFenced` |
| Errand rows | `GetErrand`, `GetErrandForUpdate`, `GetErrandByCreateKey`, `InsertErrand`, `UpdateErrand` |
| Transition rows | `InsertTransition`, `InsertOutbox`, `InsertOutboxIfAbsent`, `OpenReview`, `ResolveReview` |
| Lists | `ListOpen`, `ListRequested`, `ListDelivering`, `ListHistory`, `CourierHistory` |
| ClaimDue | `ClaimAcceptanceExpiry`, `ClaimPickupDeadline`, `ClaimDeliveryDeadline`, `ClaimVerificationLapse`, `ClaimStuckReservation`, `ClaimStuckEdit` |

Each claim query's `WHERE` is its partial index predicate from the migration
plus `column <= now`, so it is an index range scan. List queries fetch
`limit + 1` rows. The extra row only signals that another page follows.

After editing `errands.sql` or a migration, regenerate from `order-service/`:

```sh
go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate
```

## Tests

[`store_integration_test.go`](./store_integration_test.go) runs against
PostgreSQL. Each test gets its own schema with every migration applied. The
tests skip unless `TEST_DATABASE_URL` is set:

```sh
docker compose -f compose.test.yaml up -d --wait order-test-db
TEST_DATABASE_URL='postgres://order_test@127.0.0.1:5434/order_test?sslmode=disable' \
  go test -race ./internal/database/errandrepo/
```

| Test | Covers |
| --- | --- |
| `TestConcurrentAcceptCancelExpiryHasOneWinner` | F3.3.5: two accepts, a cancel and two expiry jobs race on 20 errands. Each errand leaves `OPEN` exactly once, and the losers get `ERRAND_UNAVAILABLE`. |
| `TestOutboxAndStateCommitTogether` | A failing outbox insert rolls back the errand update and earlier rows. A failing `decide` rolls back the whole `ClaimDue` batch. |
| `TestCreate` | Create rows, idempotent replay, fence states, non-Submit refusal. |
| `TestCreateWaitsForFenceAcquire` | `Create` blocks on an uncommitted Fence Acquire and is refused once it commits. |
| `TestApplyCompareAndSwap` | `ERRAND_UNAVAILABLE` vs `ERRAND_CHANGED`, pending reward edits, unknown errand. |
| `TestApplyFencesSupplierChangingEdits` | Edits onto a fenced supplier are refused. Edits keeping a fenced supplier are not. |
| `TestReviews` | Reviews opened by a courier and by the system, resolved by an admin, and the missing-review rollback. |
| `TestRecordRejection` | Rejection row payload and idempotency. |
| `TestLists` | Ordering, pagination, deadline hiding, supplier filter, limits. |
| `TestCourierHistory` | Voluntary and forced withdrawals, the `since` window. |
| `TestClaimDueSkipsLockedErrands` | A second batch skips rows locked by the first instead of waiting. |
