# Lifecycle tests (A8)

`lifecycle` holds only tests (commit A8, `test(order-lifecycle): cover
workflows and races`). They mount every user-facing errand handler (A2–A7)
on one router, as the service does, and drive errands through the HTTP API
(NFR6.1, NFR6.1.1, NFR6.1.2).

Two things that A does not serve are played by the tests themselves:

- credit-service's answers (reservation confirmed or refused), with
  `errand.Next` and `Store.Apply`, as C's consumer does;
- the deadline jobs, with `Store.ClaimDue`, as B's scheduler does.

## What is covered

| File | Covers | Store |
| --- | --- | --- |
| `workflow_test.go` | Happy paths of both roles: create → reserve → browse → accept → pick up → deliver → confirm, with every list and view on the way; withdraw and reassign; withdraw after the acceptance deadline; edit then cancel; dispute and report issue; rejected reservation | fake and PostgreSQL |
| `rules_test.go` | Self-acceptance, a stale `expectedVersion` (on accept, edit, cancel and pickup), the withdrawal restriction and when it lapses, repeated confirmation | fake and PostgreSQL |
| `transitions_test.go` | Every user action × every stored state × requester, assigned courier and another courier (297 calls). An action succeeds exactly when Diagram 1 has a rule for it and the caller has the rule's relationship. Every other call is refused with nothing written but, where the contract says so, a rejection event. Also: every route refuses a missing token, an admin-only token and a service token | fake |
| `duplicate_test.go` | A copy of a request that loses the compare-and-swap to its twin is answered `200`, for all eight actions with a retry check | fake, forced |
| `race_test.go` | Real races over HTTP: 8 couriers accepting one errand, accept vs cancel, edit vs accept, withdraw vs pickup, pickup vs the pickup-deadline job, double-tapped accept and pickup, 6 copies of one confirm. Each runs 8 times on fresh errands, and logs which side won | PostgreSQL |

The race assertions are the same in every test: exactly one winner, the
errand one version later, one transition row, credit rows written once, and
each loser a `409` whose only write is its rejection event.

## Running

The fake-store tests always run. The PostgreSQL tests skip unless
`TEST_DATABASE_URL` is set, as in `errandrepo`:

```sh
docker compose -f compose.test.yaml up -d --wait order-test-db
TEST_DATABASE_URL='postgres://order_test@127.0.0.1:5434/order_test?sslmode=disable' \
  go test -count=1 ./internal/errand/lifecycle/
```

Each test creates and drops a schema of its own, so tests and repeated runs
do not interfere.

## What A8 changed outside the tests

The race tests found that a copy of a request that lost the compare-and-swap
to an identical copy was refused. Six confirms sent at once gave one `200`
and five `409 ERRAND_UNAVAILABLE`, against F3.10.2. A double-tapped accept or
pickup also sent the courier a rejection event for an action that had
succeeded. `errandhttp.LostToDuplicate` now re-runs each handler's retry
check after a lost compare-and-swap, in `accept`, `requester` and `courier`.

## Limits

- **Races exercise interleavings; they do not enumerate them.** Every race
  saw both outcomes over repeated runs, except that accept vs cancel is
  usually won by cancel. `duplicate_test.go` forces the one interleaving
  that needed a code change.
- **Race detector not run** (no cgo on the development machine).
- **Credit-service, RabbitMQ and supplier-service are fakes** here. The
  cross-service end-to-end flow is I2's.
