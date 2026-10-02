# Supplier service

Go service for supplier and pickup-location listing, search, administration,
seed data, and immutable record versions. Planned scope and requirement IDs are
defined in [docs/product-backlog.md](docs/product-backlog.md).

The runtime registers catalogue, immutable-version, create, update, and delete
routes using local Auth0 JWT validation. DELETE is wired to the durable B5
workflow but returns `503 DELETION_FENCE_UNAVAILABLE` while the B4 order-service
fence is absent; suppliers remain available. The B6 background reconciler is
not yet implemented. See [Auth0 setup and acceptance](docs/auth0.md) for C1.
The frozen domain and HTTP decisions are documented in
[docs/contracts.md](docs/contracts.md). [openapi.yaml](openapi.yaml) composes
separate catalogue, administration, and common contract fragments.

## Technology

- Go 1.27.1
- Standard `net/http` server and `http.ServeMux` router
- PostgreSQL through `pgx`
- SQL written explicitly and generated into typed Go code with `sqlc`
- Schema migrations with `goose`
- Structured JSON logging with the standard `log/slog` package
- Standard Go tests with `testify` assertions
- OpenAPI 3.1 validated by Redocly CLI
- Docker and Docker Compose

Runtime and test library versions are declared in `go.mod` and locked by
`go.sum`. The `sqlc` and `goose` commands shown below are also version-pinned.

## Project structure

```text
supplier-service/
├── cmd/
│   └── supplier-service/
│       └── main.go
├── internal/
│   ├── app/
│   ├── auth/
│   ├── config/
│   ├── database/
│   ├── httpapi/
│   ├── logging/
│   └── supplier/
│       ├── query/
│       ├── create/
│       ├── update/
│       ├── delete/
│       └── versioning/
├── migrations/
├── queries/
├── tests/
├── .env.example
├── compose.test.yaml
├── Dockerfile
├── go.mod
├── go.sum
├── openapi.yaml
└── sqlc.yaml
```

Go's `internal` directory prevents unrelated projects from importing the
service implementation. The application is assembled separately from the
executable entry point, allowing tests to construct it without opening a
network port.

## Local setup

Install Go 1.27.1 or a compatible Go 1.27 patch release, then download the
locked modules from `supplier-service/`:

```sh
go mod download
```

`.env.example` documents settings and deployment placeholders. The executable reads process
environment variables directly; it does not automatically load `.env`. Export
the required values through your shell, IDE, container configuration, or secret
manager before starting it.

At minimum, development requires:

```text
APP_ENV=development
DATABASE_URL=postgresql://<user>:<password>@<host>:<port>/<database>
AUTH0_ISSUER=https://<your-auth0-domain>/
AUTH0_AUDIENCE=https://api.foc.local/supplier-service
SUPPLIER_SEED_CSV_PATH=../data/csv/supplier-seed-data.csv
```

Deployment/startup order (run from `supplier-service/`): apply the Goose
migrations, then start the service. Supply `GOOSE_DRIVER=postgres` and
`GOOSE_DBSTRING` through the environment, using the same database as
`DATABASE_URL` (or `TEST_DATABASE_URL` for `APP_ENV=test`). Stop deployment if
the migration command fails.

```sh
go run github.com/pressly/goose/v3/cmd/goose@v3.28.0 -dir migrations up
go run ./cmd/supplier-service
```

Startup imports the configured CSV atomically before opening HTTP. Repeated
starts skip existing seed provenance and preserve administrator edits and
deletions. Missing schema, unreadable/invalid CSV, and import failures stop
startup with a sanitized error. `STARTUP_TIMEOUT` bounds the import.

The default address is `0.0.0.0:3002`. `/readyz` is public. Catalogue and
version reads require `suppliers:read`; create, update, and delete additionally
require `suppliers:manage`. DELETE requires a UUID `Idempotency-Key` header.

## Environment variables

| Variable | Required | Purpose |
| --- | --- | --- |
| `AUTH0_ISSUER` | Yes | Exact HTTPS Auth0 issuer with trailing slash |
| `AUTH0_AUDIENCE` | No | Defaults to `https://api.foc.local/supplier-service` |
| `AUTH0_JWKS_*`, `AUTH0_CLOCK_SKEW` | No | [Cache, deadline, retry and breaker settings](docs/auth0.md) |
| `APP_ENV` | No | `development`, `test`, or `production`; defaults to `development` |
| `HTTP_HOST` | No | Server bind address; defaults to `0.0.0.0` |
| `HTTP_PORT` | No | Server port; defaults to `3002` |
| `LOG_LEVEL` | No | `debug`, `info`, `warn`, or `error`; defaults to `info` |
| `SHUTDOWN_TIMEOUT` | No | Graceful shutdown duration; defaults to `10s` |
| `STARTUP_TIMEOUT` | No | Seed initialization deadline; defaults to `30s` |
| `DATABASE_URL` | Development/production | PostgreSQL connection URL |
| `TEST_DATABASE_URL` | Test | Isolated PostgreSQL test connection URL |
| `DATABASE_POOL_MIN` | No | Minimum idle pool size; defaults to `1` |
| `DATABASE_POOL_MAX` | No | Maximum pool size; defaults to `10` |
| `SUPPLIER_SEED_DATASET_NAMESPACE` | No | Required seed provenance namespace; defaults to `template-v1` |
| `SUPPLIER_SEED_CSV_PATH` | Yes | Deployment-provided CSV path; relative to the working directory or absolute |

Invalid configuration stops startup. Error messages identify invalid variable
names but do not include their values, preventing credentials from entering
logs.

## Tests

Run all mandatory tests with:

```sh
go test ./...
```

The command exits nonzero when any test fails, satisfying NFR6.2. Signed-token
route tests use a local TLS JWKS fixture. Startup and HTTP/database integration
tests run when `TEST_DATABASE_URL` is set, each in its own disposable schema;
they verify seed initialization, repeat startup, and composed endpoints. Configuration
tests verify that `APP_ENV=test` selects `TEST_DATABASE_URL` and does not leak
configuration values in errors.

`/readyz` returns ready only when PostgreSQL is reachable, all supplier tables
exist, the configured seed namespace has provenance, and Auth0 verification
keys are cached within their lifetime or can be fetched. The seed import is
atomic, so any provenance row for that namespace represents a completed import,
not a partial dataset.

Readiness currently covers database/seed state and Auth0 keys. It does not
claim deletion availability while B4/B6 are outstanding.

In CI environments with CGO and a C compiler available, also run the race
detector:

```sh
go test -race ./...
```

Run static analysis separately with:

```sh
go vet ./...
```

Validate the OpenAPI contract with:

```sh
npx --yes @redocly/cli@2.54.1 lint openapi.yaml
```

### Preview the OpenAPI documentation

From the `supplier-service/` directory, start a local Redoc documentation
preview with:

```sh
npx --yes @redocly/cli@1.34.5 preview-docs openapi.yaml
```

Open the URL printed by the command, typically
`http://127.0.0.1:8080`. The preview renders the endpoints, parameters,
request and response schemas, authentication requirements, and examples in a
browser-friendly format. It also watches `openapi.yaml` and its referenced
files under `openapi/` and refreshes when they change.

The preview command intentionally uses Redocly CLI v1 because the
`preview-docs` command was removed in Redocly CLI v2. Continue to use the
version-pinned v2 command above for contract validation.

## Test database

Start the disposable PostgreSQL database from the repository root:

```sh
docker compose -f supplier-service/compose.test.yaml up -d
```

Configure integration tests with:

```text
APP_ENV=test
TEST_DATABASE_URL=postgresql://supplier_test@127.0.0.1:5433/supplier_test
```

The database binds only to the local machine, persists data in a named local
Docker volume, and uses trust authentication. It is strictly for automated
local testing. The named test volume allows the restart test to exercise a
real PostgreSQL restart. Stop it
with:

```sh
docker compose -f supplier-service/compose.test.yaml down
```

Remove the disposable data volume when a completely fresh database is needed:

```sh
docker compose -f supplier-service/compose.test.yaml down -v
```

With the test database running and `TEST_DATABASE_URL` set, repository, seed,
readiness, and 1,000-record pagination integration tests run as part of
`go test ./...`. Run the repeatable capacity benchmark from
`supplier-service/` with:

```sh
go test -run '^$' -bench '^BenchmarkListSearchPagination1000$' ./internal/database/supplierrepo
```

This data-layer benchmark is a repeatable capacity check; the integrated
100-user SLO workload and pass/fail decision remain part of I3.

The database restart test is exclusive because it restarts the Compose test
database. Run it without other database tests:

```sh
RUN_DATABASE_RESTART_TEST=1 go test -count=1 -run '^TestPersistenceSurvivesDatabaseAndServiceRestart$' ./internal/database/supplierrepo
```

The test creates two immutable supplier versions, seed provenance, and a
pending deletion operation, restarts PostgreSQL, constructs a fresh connection
pool and repository (the service restart), and verifies all three remain.

## Database tooling

Add Goose migrations under `migrations/` and sqlc query definitions under
`queries/`. Generate typed query code using the pinned sqlc version:

```sh
go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate
```

Run migrations by providing Goose with `GOOSE_DRIVER=postgres` and a
`GOOSE_DBSTRING` from the appropriate environment, then invoking:

```sh
go run github.com/pressly/goose/v3/cmd/goose@v3.28.0 -dir migrations up
```

Do not place database credentials in scripts or source-controlled files.

## Container build

Build the production image from the repository root:

```sh
docker build -t foc-supplier-service supplier-service
```

Inject runtime configuration when starting the container. The image runs as an
unprivileged user and contains no `.env` files or build toolchain.
Apply migrations as the deployment step above before launching the container.
Mount `data/csv/supplier-seed-data.csv` read-only and set
`SUPPLIER_SEED_CSV_PATH` to its absolute container path. The CSV is not bundled
in the service image because the build context is `supplier-service/`.

## Incremental API workflow

For each API operation:

1. Select a requirement from the product backlog.
2. Implement the already-frozen endpoint contract, changing it only through an
   explicit cross-team contract review.
3. Add focused tests for the contract and use case.
4. Implement the operation in the corresponding supplier feature package.
5. Run tests, `go vet`, and contract validation before committing.

Keep stable supplier IDs, immutable version IDs, soft deletion, and historical
reads aligned with [docs/contracts.md](docs/contracts.md).
