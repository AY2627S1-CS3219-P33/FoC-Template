# Order service

Go service for the Friend on Campus errand lifecycle: requesting, accepting,
delivering and reviewing errands, plus administration and service-to-service
routes. The HTTP contract is [openapi.yaml](openapi.yaml), which composes the
fragments under [openapi/](openapi/). Messaging, idempotency, locking and cache
rules are in [docs/contracts.md](docs/contracts.md).

The service has no executable entry point or Dockerfile yet. The domain, ports,
test fakes, auth middleware, resilience helpers, schema and generated queries
are in place and covered by tests.

## Project structure

```text
order-service/
├── docs/contracts.md       # Shared contracts with other services
├── internal/
│   ├── apperror/           # Error codes returned by the API
│   ├── auth/               # Auth0 JWT validation and middleware
│   ├── database/           # sqlc output (generated, not committed)
│   ├── errand/             # Domain types, state machine, outbox, ports
│   ├── httpapi/            # Router
│   ├── resilience/         # Retry, backoff and circuit breaker
│   └── testfakes/          # In-memory fakes for the ports
├── migrations/             # Goose SQL migrations
├── openapi/                # OpenAPI fragments
├── queries/                # SQL for sqlc
├── compose.test.yaml       # Test PostgreSQL, RabbitMQ and Redis
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
(`redis://127.0.0.1:6380/0`) for the integration tests that will use
`TEST_AMQP_URL` and `TEST_REDIS_URL`. Override a host port that is already
taken with `ORDER_TEST_DB_PORT`, `ORDER_TEST_AMQP_PORT`,
`ORDER_TEST_AMQP_UI_PORT` or `ORDER_TEST_REDIS_PORT`, and update the matching
URL.

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

`auth.Auth0Config` holds the Auth0 token validation settings. Its fields are
named after the environment variables below, which the entry point will read
once it exists. `DefaultAuth0Config()` supplies the defaults and `Validate()`
enforces the limits.

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
