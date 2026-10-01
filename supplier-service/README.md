# Supplier service

Go service for supplier and pickup-location listing, search, administration,
seed data, and immutable record versions. Planned scope and requirement IDs are
defined in [docs/product-backlog.md](docs/product-backlog.md).

This scaffold provides the application shell and development infrastructure.
The frozen domain and HTTP decisions are documented in
[docs/contracts.md](docs/contracts.md). [openapi.yaml](openapi.yaml) composes
separate catalogue, administration, and common contract fragments; business
operations are contracts only until their feature handlers are implemented.

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

`.env.example` contains placeholders only. The executable reads process
environment variables directly; it does not automatically load `.env`. Export
the required values through your shell, IDE, container configuration, or secret
manager before starting it.

At minimum, development requires:

```text
APP_ENV=development
DATABASE_URL=postgresql://<user>:<password>@<host>:<port>/<database>
```

Run the service with:

```sh
go run ./cmd/supplier-service
```

The default address is `0.0.0.0:3002`. `/readyz` is implemented; the supplier
routes are frozen in OpenAPI and will be registered by their feature packages.

## Environment variables

| Variable | Required | Purpose |
| --- | --- | --- |
| `APP_ENV` | No | `development`, `test`, or `production`; defaults to `development` |
| `HTTP_HOST` | No | Server bind address; defaults to `0.0.0.0` |
| `HTTP_PORT` | No | Server port; defaults to `3002` |
| `LOG_LEVEL` | No | `debug`, `info`, `warn`, or `error`; defaults to `info` |
| `SHUTDOWN_TIMEOUT` | No | Graceful shutdown duration; defaults to `10s` |
| `DATABASE_URL` | Development/production | PostgreSQL connection URL |
| `TEST_DATABASE_URL` | Test | Isolated PostgreSQL test connection URL |
| `DATABASE_POOL_MIN` | No | Minimum idle pool size; defaults to `1` |
| `DATABASE_POOL_MAX` | No | Maximum pool size; defaults to `10` |
| `SUPPLIER_SEED_DATASET_NAMESPACE` | No | Required seed provenance namespace; defaults to `template-v1` |

Invalid configuration stops startup. Error messages identify invalid variable
names but do not include their values, preventing credentials from entering
logs.

## Tests

Run all mandatory tests with:

```sh
go test ./...
```

The command exits nonzero when any test fails, satisfying NFR6.2. The startup
test constructs the complete application, exercises its HTTP handler, and
closes it without opening a port or connecting to PostgreSQL. Configuration
tests verify that `APP_ENV=test` selects `TEST_DATABASE_URL` and does not leak
configuration values in errors.

`/readyz` returns ready only when PostgreSQL is reachable, all supplier tables
exist, and the configured seed namespace has provenance. The seed import is
atomic, so any provenance row for that namespace represents a completed import,
not a partial dataset.

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
