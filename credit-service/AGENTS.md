# Credit service agent notes

## Scope
This service owns credit lifecycle logic: initial allocation, balance lookup, reservation, edit, release/refund and transfer to a courier. Order and supplier logic must not live here. Never query another service's database; expose an API instead.

## Requirements
Idempotency for allocations, reservations, releases and transfers (NFR7.3); records must survive container restarts (NFR4.1.3). Credits are whole numbers (`bigint`, never floats); serialise balances as strings in APIs so user-service's exact-decimal contract still holds.

## Conventions
Follow `supplier-service`: env-only config in `internal/config`, handler assembled in `internal/app`, goose migrations in `migrations/` applied only by `infra/migrate.sh` as `migrator` (the service runs as `app_rw` and must never migrate), no secrets or connection URLs in errors or logs.

## Data model rules
See `docs/wallet-aggregate.md`. In short: change balances only through `internal/database/walletrepo` inside one transaction, locking wallets first with `LockWalletsByAuth0Subject`; write a `ledger_entries` row for every balance change with an idempotency key; `ledger_entries` is append-only; never write business rules into the repository layer.

## Verify
```sh
gofmt -l . && go vet ./... && go test ./... && go build ./...
```

## Database roles
Never put `.env.admin`, master or migrator credentials into the service environment, compose file or image. Every new migration is a new file; never edit an applied one.

## Authentication
Users and services reach different routes: a user token only on `/v1/wallet/*`, a machine token only on `/internal/*`. Take identity and permissions from the verified token (`auth.PrincipalFromContext`), never from the request. Order-related changes will arrive as RabbitMQ events authenticated by broker credentials, so never put tokens in messages. The initial allocation amount is configuration (`INITIAL_CREDIT_AMOUNT`), never a request field. Use `go test -p 1 ./...` for integration tests.
