# Credit service

Owns user credit state: balances, reservations, refunds and transfers. Credits are a closed economy; they cannot be bought, withdrawn or exchanged for money. Currently implemented: wallet creation (with the configured initial allocation), balance reads, Auth0 token authentication, the wallet schema and its SQL data-access layer. Order-related operations (reserve, adjust, refund, transfer) will arrive as RabbitMQ events and are not built yet. See [docs/wallet-aggregate.md](docs/wallet-aggregate.md) for the data model and rules.

## Technology

Go, `net/http`, `pgx/v5`, `goose` (migrations applied out of band as `migrator`), `log/slog` JSON logs, Amazon RDS PostgreSQL in deployment.

## Project structure

```
cmd/credit-service/   entry point
internal/app/         HTTP handler and server lifecycle
internal/auth/        Auth0 JWT verification and permission middleware
internal/wallet/      wallet use cases (create, read)
internal/walletapi/   HTTP handlers for wallets
internal/apperror/    JSON error body
internal/testauth/    local signed-token fixtures for tests
internal/config/      environment configuration
internal/database/    pgx pool
internal/database/walletrepo/  SQL for wallets, reservations, ledger
internal/testdb/      migrated test database helper
internal/readiness/   GET /readyz
internal/logging/     slog setup
migrations/           goose SQL migrations (applied by infra/migrate.sh)
infra/                bootstrap.sql, bootstrap.sh, migrate.sh
```

## Database setup (Amazon RDS)

Three database roles keep the running service least-privileged:

| Role | Used by | Can |
|---|---|---|
| master (`postgres`) | `infra/bootstrap.sh`, once | create roles and the database |
| `migrator` | `infra/migrate.sh` | create and alter tables |
| `app_rw` | the service (`DATABASE_URL`) | read and write rows only |

Because `app_rw` has no schema rights, the service does **not** run migrations on startup.

```sh
cp .env.admin.example .env.admin   # fill in master password, APP_DB and role passwords
./infra/bootstrap.sh               # once per RDS instance
./infra/migrate.sh                 # whenever a migration is added; ./infra/migrate.sh status to inspect
```

`.env.admin` is gitignored and must never be given to the service.

The RDS instance must be reachable from where you run the scripts and the service: its security group needs an inbound PostgreSQL (5432) rule for your IP or the service's security group, and Public access must be Yes for access from a home network.

## Running the service

```sh
cp .env.example .env              # set DATABASE_URL to the app_rw URL
go run ./cmd/credit-service       # loads .env if present; or: docker compose up --build
curl localhost:3004/readyz        # {"status":"ready"}
```

`/readyz` returns 503 if the database is unreachable or its schema is older than the migrations shipped in the build, so a forgotten `./infra/migrate.sh` is visible immediately.

## API

| Route | Caller | Permission | Purpose |
|---|---|---|---|
| `GET /v1/wallet/me` | user (login token) | `credits:read:own` | Read the wallet of the token's own subject. Takes no ID. |
| `PUT /internal/wallets/{ownerId}` | user-service (machine token) | `credits:wallets:create` | Create the wallet with body `{"auth0Subject": "..."}` and apply `INITIAL_CREDIT_AMOUNT`. Idempotent: `201` when created, `200` if it already exists, `409` if the subject differs or belongs to another wallet. |
| `GET /internal/wallets/{ownerId}` | user-service (machine token) | `credits:read` | Read any wallet. |
| `GET /readyz` | anyone | none | Database and schema version check. |

`ownerId` is the user-service account UUID. Balances are returned as strings of whole credits (`"available":"100"`). A user token is refused on `/internal` routes and a machine token on `/v1/wallet/me`, even if the token carries the other permission.

Auth0 setup: create an API with identifier `https://api.foc.local/credit-service`, enable RBAC and "Add Permissions in the Access Token", define the three permissions above, give users only `credits:read:own`, and authorise user-service's machine-to-machine app for the other two.

## Environment variables

| Variable | Required | Purpose |
|---|---|---|
| `DATABASE_URL` | Yes | PostgreSQL URL for the `app_rw` role (RDS endpoint) |
| `TEST_DATABASE_URL` | When `APP_ENV=test` | Test database URL |
| `INITIAL_CREDIT_AMOUNT` | Yes | Whole credits each new wallet starts with (0 to 1,000,000) |
| `AUTH0_ISSUER` | Yes | Auth0 tenant URL, e.g. `https://tenant.auth0.com/` |
| `AUTH0_AUDIENCE` | No | Default `https://api.foc.local/credit-service` |
| `APP_ENV` | No | `development` (default), `test`, `production` |
| `HTTP_HOST` / `HTTP_PORT` | No | Listen address, default `0.0.0.0:3004` |
| `LOG_LEVEL` | No | `debug`, `info` (default), `warn`, `error` |
| `SHUTDOWN_TIMEOUT` / `STARTUP_TIMEOUT` | No | Defaults `10s` / `30s` |
| `DATABASE_POOL_MIN` / `DATABASE_POOL_MAX` | No | Defaults `1` / `10` |

## Tests

```sh
gofmt -l . && go vet ./... && go test ./... && go build ./...
```

Integration tests (`*_integration_test.go`) need the throwaway database and skip themselves without it. Use `-p 1`, because the packages share one database and empty its tables:

```sh
docker compose -f compose.test.yaml up -d
TEST_DATABASE_URL='postgres://credit_test@127.0.0.1:5434/credit_test?sslmode=disable' go test -p 1 ./...
```

HTTP tests call `app.Handler` through `httptest`, so they open no network port.
