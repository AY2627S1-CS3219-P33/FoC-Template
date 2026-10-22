# Order service

Go service for the Friend on Campus errand lifecycle: requesting, accepting,
delivering and reviewing errands, plus administration and service-to-service
routes. The HTTP contract is [openapi.yaml](openapi.yaml), which composes the
fragments under [openapi/](openapi/). Messaging, idempotency, locking and cache
rules are in [docs/contracts.md](docs/contracts.md).

`cmd/order-service` is the service: one process serves every HTTP route and
runs the background workers (outbox dispatcher, credit-result consumer, credit
reconcilers, deadline and reminder scheduler). See
[Running the service](#running-the-service).

## Project structure

```text
order-service/
├── cmd/order-service/       # Service entry point
├── cmd/outbox-worker/       # Standalone RabbitMQ outbox dispatcher
├── docs/contracts.md       # Shared contracts with other services
├── internal/
│   ├── app/                # Composition root: adapters, routes, workers
│   ├── apperror/           # Error codes returned by the API
│   ├── auth/               # Auth0 JWT validation and middleware
│   ├── cache/              # Redis and no-op read caches
│   ├── config/             # Environment variables
│   ├── creditmsg/          # Credit-result consumer
│   ├── database/           # Repositories and generated sqlc packages
│   ├── errand/             # Domain, state machine, ports, errand routes
│   ├── fence/              # Supplier deletion fence routes
│   ├── httpapi/            # Router
│   ├── internalapi/        # Service-to-service read routes
│   ├── messaging/          # RabbitMQ publisher and outbox dispatcher
│   ├── outboxadmin/        # Failed outbox rows and requeue
│   ├── readiness/          # /readyz
│   ├── reconciler/         # Stuck reservations and edits
│   ├── resilience/         # Retry, backoff and circuit breaker
│   ├── review/             # Admin reviews, settings, event log, flags
│   ├── scheduler/          # Deadline and reminder jobs
│   ├── serviceauth/        # Service tokens, in and out
│   ├── supplierclient/     # supplier-service adapter
│   └── testfakes/          # In-memory fakes for the ports
├── migrations/             # Goose SQL migrations
├── openapi/                # OpenAPI fragments
├── queries/                # SQL for sqlc
├── compose.yaml            # order-service, order-db, order-redis
├── compose.test.yaml       # Test PostgreSQL, RabbitMQ and Redis
├── Dockerfile
├── openapi.yaml
└── sqlc.yaml
```

## Setup

Install Go 1.27, then from `order-service/`:

```sh
go mod download
go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate
```

The sqlc packages under `internal/database/` are excluded from Git, so run
`generate` after a fresh checkout and whenever `migrations/` or `queries/`
change. The build fails without them.

## OpenAPI contract

Preview the rendered documentation in a browser from `order-service/`:

```sh
npx --yes @redocly/cli@1.34.5 preview-docs openapi.yaml --config docs/preview/redocly.yaml
```

Open the printed URL, usually `http://127.0.0.1:8080`. The page reloads when
`openapi.yaml` or any file under `openapi/` changes. `preview-docs` needs
Redocly CLI v1 because v2 removed it.

The `--config` flag is required. Without it the CLI loads `redoc/latest` from
the CDN, whose current bundle fails with `Cannot use 'import.meta' outside a
module` and leaves the page blank. The custom template in `docs/preview/`
swaps in Redoc v2.5.4, the version `build-docs` pins.

To produce a single standalone HTML file instead, for example to share it:

```sh
npx --yes @redocly/cli@2.54.1 build-docs openapi.yaml -o openapi.html
```

`openapi.html` is ignored by Git.

Validate the contract before committing changes to it:

```sh
npx --yes @redocly/cli@2.54.1 lint openapi.yaml
```

## Tests

```sh
go test ./...
go test -race ./...
go vet ./...
```

Database tests skip unless `TEST_DATABASE_URL` is set. Start the test
dependencies from the repository root:

```sh
docker compose -f order-service/compose.test.yaml up -d --wait
```

Then, from `order-service/`:

```sh
# bash
export TEST_DATABASE_URL='postgres://order_test@127.0.0.1:5434/order_test?sslmode=disable'
go test ./...
```

```powershell
# PowerShell
$env:TEST_DATABASE_URL = 'postgres://order_test@127.0.0.1:5434/order_test?sslmode=disable'
go test ./...
```

The compose file also starts RabbitMQ (`amqp://guest:guest@127.0.0.1:5673/`,
management UI at `http://127.0.0.1:15673`) and Redis
(`redis://127.0.0.1:6380/0`) for the integration tests that use
`TEST_AMQP_URL` and `TEST_REDIS_URL`. Override a host port that is already
taken with `ORDER_TEST_DB_PORT`, `ORDER_TEST_AMQP_PORT`,
`ORDER_TEST_AMQP_UI_PORT` or `ORDER_TEST_REDIS_PORT`, and update the matching
URL.

Run the outbox tests against PostgreSQL and RabbitMQ, from `order-service/`:

```sh
TEST_DATABASE_URL='postgres://order_test@127.0.0.1:5434/order_test?sslmode=disable' \
TEST_AMQP_URL='amqp://guest:guest@127.0.0.1:5673/' \
go test -race ./internal/messaging
```

Use the dedicated test broker; the unroutable-command test expects no existing
credit-service command binding. Tests create isolated PostgreSQL schemas and
temporary consumer queues. The order-owned results queue and DLQ remain on the
test broker.

## Running the service

The root `compose.yaml` includes [compose.yaml](compose.yaml), which defines:

| Container | Image | Networks |
| --- | --- | --- |
| `order-service` | built from [Dockerfile](Dockerfile), port `127.0.0.1:3003` | `default`, `order-internal` |
| `order-db` | `postgres:17-alpine`, volume `order-db-data` | `order-internal` |
| `order-redis` | `redis:7.4-alpine`, no persistence, `maxmemory 128mb`, `allkeys-lru` | `order-internal` |
| `order-migrate` | Goose, runs `up` once and exits | `order-internal` |

`order-internal` is an internal network: only order-service reaches its
database and cache, and neither publishes a port. `default` is the root
project's network, shared with `queue-service` (RabbitMQ) and
`supplier-service`. order-service starts after `order-db` is healthy and the
migrations have run, but only waits for `order-redis` to start, because Redis
is optional.

1. Copy `.env.example` to `.env` and fill in the Auth0 issuer and the M2M
   client ID and secret. Compose overrides the addresses with the container
   ones.
2. supplier-service must trust the same Auth0 tenant, or it rejects
   order-service's token and errand creation fails with
   `DEPENDENCY_UNAVAILABLE`. Set `SUPPLIER_AUTH0_ISSUER` in the root `.env`.
3. From the repository root:

   ```sh
   docker compose up -d --build --wait order-service supplier-service queue-service
   curl http://127.0.0.1:3003/readyz
   ```

`/readyz` returns `503` only when PostgreSQL is unreachable. Redis, RabbitMQ,
the credit-result consumer and the supplier-service breaker are reported, and
anything unhealthy makes the status `degraded`.

On startup the service checks PostgreSQL and fails if it cannot connect. It
connects to Redis and RabbitMQ lazily and keeps retrying in the background.
On `SIGTERM` it stops accepting requests, lets in-flight requests finish, and
waits for every worker to finish its current batch or delivery, all within
`SHUTDOWN_TIMEOUT`.

To run it on the host instead, start the test dependencies, apply the
migrations (see below), and use host addresses in `.env`:

```sh
go run ./cmd/order-service
```

### RabbitMQ topology

On its first connection order-service declares the `foc.events` (topic) and
`foc.commands` (direct) exchanges, its `order-service.credit-results` queue
(bound to `credit.#`, dead-lettering to `order-service.credit-results.dlq`
after 5 deliveries), and, with `AMQP_DECLARE_TAP=true`, `order-events.tap`
bound to `errand.#`. Credit-service owns `credit-service.commands`; until it
declares that queue, credit commands are unroutable and stay in the outbox,
retrying. Publication is mandatory, so leave `AMQP_DECLARE_TAP=true` until a
notification consumer binds `errand.*`, or events are retried until `FAILED`.

## Outbox worker

The service runs the dispatcher itself. This standalone worker publishes the
same outbox rows without serving HTTP; running both is safe, because rows are
claimed under a lease.

Generate the sqlc packages and apply migrations first, using the setup and
migration commands in this README. Then run, from `order-service/`:

```sh
DATABASE_URL='postgres://order_test@127.0.0.1:5434/order_test?sslmode=disable' \
AMQP_URL='amqp://guest:guest@127.0.0.1:5673/' \
AMQP_DECLARE_TAP=true \
go run ./cmd/outbox-worker
```

`DATABASE_URL` and `AMQP_URL` are required. `AMQP_DECLARE_TAP` is optional
(default `false`); enable it locally to retain errand events in `order-events.tap`
until notification-service declares its queues. Inspect that queue in the
RabbitMQ management UI. For the root Compose broker, applications on the host
use `127.0.0.1:5672` and its configured credentials; applications in its network
use `queue-service:5672`.

The RabbitMQ connection and topology are created on the first publication.
An idle worker with an empty outbox does not connect to the broker yet.

The worker has independent credit and event loops. Each claims batches of up
to 10 rows under a two-minute lease, publishes each with a ten-second timeout,
and polls again at once after a full batch or after about a second (±20%
jitter) otherwise. Failed publishes use the existing jittered backoff (2 seconds
base, 5 minutes cap), with at most 10 publication attempts. A failed broker
connection is retried after its own backoff (500 ms base, 30 s cap) rather
than on every row. Once a minute a sweep re-sends `SENT` credit commands that
have had no result for the confirmation timeout from stored settings (default
10 minutes), and records the backlog. Messages retain their original IDs on
retry. Expired claims recover automatically; writes from old claims cannot
overwrite a newer claim or credit confirmation. On `SIGINT`/`SIGTERM` each loop
finishes the row it is publishing and hands the rest of its batch back.

Both commands and events use mandatory publishing and broker confirmations.
Without a matching queue binding they retry instead of becoming `SENT`.
Credit-service must declare its own command queue; the worker does not create
it. Configure consumer queues before starting normal publication.

This worker does not create errands or process credit replies. Credit commands
remain `SENT` until the service's credit-result consumer confirms them, and are
re-sent after the confirmation timeout. Each resend consumes another attempt;
exhausted rows become `FAILED`.

Stop the dependencies, removing the test database volume:

```sh
docker compose -f order-service/compose.test.yaml down -v
```

## Database migrations

Migrations are applied with Goose before the service starts; the service never
runs DDL itself. See [migrations/README.md](migrations/README.md). For the
local test database:

```sh
export GOOSE_DRIVER=postgres
export GOOSE_DBSTRING='postgres://order_test@127.0.0.1:5434/order_test?sslmode=disable'
go run github.com/pressly/goose/v3/cmd/goose@v3.28.0 -dir migrations up
go run github.com/pressly/goose/v3/cmd/goose@v3.28.0 -dir migrations status
```

After adding a migration or changing a query, regenerate the sqlc code.

## Configuration

`internal/config` reads every variable at startup and reports all problems at
once, without echoing values. [.env.example](.env.example) lists them.

| Variable | Default | Allowed |
| --- | --- | --- |
| `APP_ENV` | `development` | `development`, `test` (reads `TEST_DATABASE_URL`), `production` |
| `HTTP_HOST`, `HTTP_PORT` | `0.0.0.0`, `3003` | |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `STARTUP_TIMEOUT`, `SHUTDOWN_TIMEOUT` | `30s`, `10s` | Positive durations |
| `DATABASE_URL` | none | PostgreSQL URL |
| `DATABASE_POOL_MIN`, `DATABASE_POOL_MAX` | `1`, `10` | Min at most max |
| `ORDER_M2M_CLIENT_ID`, `ORDER_M2M_CLIENT_SECRET` | none | Auth0 M2M application in the `AUTH0_ISSUER` tenant |
| `SUPPLIER_AUDIENCE` | `https://api.foc.local/supplier-service` | |
| `SUPPLIER_SERVICE_URL` | none | Absolute http(s) URL |
| `SUPPLIER_SERVICE_TIMEOUT` | `2s` | Positive duration |
| `REDIS_URL` | empty: no-op cache | `redis://` or `rediss://` URL |
| `AMQP_URL` | none; startup fails without it | `amqp://` or `amqps://` URL |
| `AMQP_DECLARE_TAP` | `false` | Boolean |
| `CREDIT_RESULTS_PREFETCH` | `16` | `1` to `1000` |

`auth.Auth0Config` holds the Auth0 token validation settings.
`DefaultAuth0Config()` supplies the defaults and `Validate()` enforces the
limits.

| Variable | Default | Allowed |
| --- | --- | --- |
| `AUTH0_ISSUER` | none | HTTPS origin with a trailing slash |
| `AUTH0_AUDIENCE` | `https://api.foc.local/order-service` | Nonempty, no spaces or commas |
| `AUTH0_CLOCK_SKEW` | `30s` | `0s` to `1m` |
| `AUTH0_JWKS_CACHE_TTL` | `5m` | Above `0`, at most `1h` |
| `AUTH0_JWKS_FETCH_TIMEOUT` | `3s` | Above `0`, at most `10s` |
| `AUTH0_JWKS_REFRESH_INTERVAL` | `10s` | Above `0`, at most the cache TTL |
| `AUTH0_JWKS_FETCH_ATTEMPTS` | `1` | `1` to `3` |
| `AUTH0_JWKS_BREAKER_THRESHOLD` | `3` | `1` to `100` |
| `AUTH0_JWKS_BREAKER_COOLDOWN` | `30s` | Above `0`, at most `1h` |

See [internal/auth/middleware.md](internal/auth/middleware.md) for how the
middleware uses them.
