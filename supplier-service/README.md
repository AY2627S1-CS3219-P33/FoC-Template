# Supplier service

Go service for supplier and pickup-location listing, search, administration,
seed data, and immutable record versions. Planned scope and requirement IDs are
defined in [docs/product-backlog.md](docs/product-backlog.md).

This scaffold provides the application shell and development infrastructure. It
does not implement API operations yet, so [openapi.yaml](openapi.yaml) remains
an empty contract template.

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

The default address is `0.0.0.0:3002`. No routes are registered by this
scaffold.

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

In CI environments with CGO and a C compiler available, also run the race
detector:

```sh
go test -race ./...
```

Run static analysis separately with:

```sh
go vet ./...
```

Validate the incremental OpenAPI contract with:

```sh
npx --yes @redocly/cli@2.54.1 lint openapi.yaml
```

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

The database binds only to the local machine, stores its data in memory, and
uses trust authentication. It is strictly for automated local testing. Stop it
with:

```sh
docker compose -f supplier-service/compose.test.yaml down
```

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
2. Add the endpoint, schemas, security, and errors to `openapi.yaml`.
3. Add focused tests for the contract and use case.
4. Implement the operation in the corresponding supplier feature package.
5. Run tests, `go vet`, and contract validation before committing.

Plan stable supplier IDs and immutable version IDs in the database model before
implementing CRUD operations, even though endpoints will be added incrementally.
