<!--
AI Assistance Disclosure:
Tool: Claude Code (model: Claude Opus 5.5), date: 2026-09-03
Scope: Documentation writing and formatting.
Author review: Reviewed, validated, and tested by the authors.
-->

# Supplier service

Go service for supplier and pickup-location listing, search, administration,
seed data, and immutable record versions. Planned scope and requirement IDs are
defined in [docs/product-backlog.md](docs/product-backlog.md).

The runtime registers catalogue, immutable-version, create, update, and delete
routes using local Auth0 JWT validation. DELETE runs the durable B5 workflow
against order-service's supplier fence through the B4 client
([`internal/orderclient`](internal/orderclient/client.go)), authenticated with
supplier-service's own Auth0 M2M token (`orders:internal`). The B6 reconciler
resumes unfinished deletions in the background. Deletion is enabled only when
`ORDER_SERVICE_URL` and the `SUPPLIER_M2M_*` credentials are set; otherwise
DELETE fails closed with `503 DELETION_FENCE_UNAVAILABLE` and suppliers remain
available. See [Auth0 setup and acceptance](docs/auth0.md) for C1.
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
| `SUPPLIER_SEED_CSV_PATH` | No | Defaults to the CSV bundled at `/app/data/supplier-seed-data.csv` in the container image; can be overridden |

Invalid configuration stops startup. Error messages identify invalid variable
names but do not include their values, preventing credentials from entering
logs.

## Tests

From `supplier-service/`, generate the database query code and run all mandatory tests:

```sh
go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate
go test ./...
```

The generated package is excluded from Git. Run generation after a fresh checkout
and whenever migrations or SQL queries change; `go test` does not generate it.

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
depend on order-service: catalogue reads stay available while it is down, and
only deletion fails closed.

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

If port 5433 is already taken (for example by the e2e stack), choose another
host port and use it in `TEST_DATABASE_URL` below:

```sh
SUPPLIER_TEST_DB_PORT=5434 docker compose -f supplier-service/compose.test.yaml up -d
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
docker build -t foc-supplier-service -f supplier-service/Dockerfile .
```

Inject runtime configuration when starting the container. The image runs as an
unprivileged user and contains no `.env` files or build toolchain.
Apply migrations as the deployment step above before launching the container.
The image bundles `data/csv/supplier-seed-data.csv` at
`/app/data/supplier-seed-data.csv`; override `SUPPLIER_SEED_CSV_PATH` only when
you need to supply a different deployment dataset.

## Supplier image URLs and existing databases

The seed CSV stores root-relative image paths such as `/ANNA.jpeg` for all
21 template suppliers (F2.1.1, F2.2.1, F2.3.1). Set
`SUPPLIER_IMAGE_BASE_URL` to your HTTPS image origin, for example
`https://your-distribution.cloudfront.net`, in the root `.env` for Compose or
the service environment for standalone runs. The importer combines the base
URL and path before storing a complete URL in the database. Absolute HTTP(S)
URLs and blank image fields remain supported in custom seed files. Relative
paths require a base URL; missing configuration fails startup before writes.
The browser reads images through CloudFront; supplier-service stores their
URLs, not image bytes. No distribution domain is embedded in the seed data.

Fresh databases receive these URLs through normal seeding. Existing databases
retain their current records, including administrator edits. To update the
template suppliers' image URLs, run from the repository root with Python 3:

```sh
python3 supplier-service/scripts/backfill_images.py
python3 supplier-service/scripts/backfill_images.py --apply
```

For the Python script, **export** `SUPPLIER_IMAGE_BASE_URL` with the same value
as the service configuration before running either command. Unlike Compose,
the script does not automatically load `.env` files:

```sh
export SUPPLIER_IMAGE_BASE_URL=https://your-distribution.cloudfront.net
```

Each invocation prompts for an Auth0 **access token for the supplier-service
API**, with both `suppliers:read` and `suppliers:manage` permissions. The prompt
hides input; do not use an ID token or share the token in chat. For example,
obtain the Bearer token from a supplier management request in your local
browser's Network panel after signing in as an administrator. Automation may
provide `SUPPLIER_API_TOKEN` through its secret environment instead.

The default API URL is `http://localhost:8081/supplier-service`, through the
Compose frontend proxy. Use `--api-url` for another deployment; HTTPS is
required except on loopback addresses. Both commands require the stack to be
running. The first command only previews changes. Review that output before
running with `--apply`, which replaces existing image URLs for the matching
seeded suppliers, even if they were previously edited by an administrator.

The script reads every page and matches exact CSV supplier names. Missing or
ambiguous names stop the run before any writes. Matching image URLs are
skipped, and non-template suppliers are untouched. PATCH requests update only
`imageUrl` through the existing API, preserving supplier version history. Old
versions retain their original image URLs. If a request fails, earlier updates
remain saved; rerun the preview to check progress before applying again. Avoid
concurrent supplier edits during the backfill. This script does not verify the
remote images or upload files to S3.

Changing `SUPPLIER_IMAGE_BASE_URL` affects newly seeded records, not URLs
already persisted in the database. Use this backfill to update existing
template suppliers when changing image hosts.

Verify the backfill script without contacting the API or AWS:

```sh
python3 -m unittest discover -s supplier-service/scripts -p 'test_*.py'
```

### Administrator image uploads

The supplier create/edit form accepts JPEG or PNG uploads, shows the uploaded
image, and supports removing the image reference. `POST /suppliers/images`
requires both `suppliers:read` and `suppliers:manage`. Send exactly one file
in the multipart `image` field. The backend verifies the actual format,
decodes the image, and enforces a 5 MiB file limit and a 16-million-pixel limit.
The frontend proxy allows a 6 MiB body to accommodate multipart overhead.

Configure these values in root `.env` for Compose:

```dotenv
SUPPLIER_IMAGE_BUCKET=foc-supplier-images
AWS_REGION=ap-southeast-1
SUPPLIER_IMAGE_BASE_URL=https://your-distribution.cloudfront.net
```

An empty bucket disables uploads with HTTP 503 without disabling the catalogue.
Each upload uses a generated `suppliers/<random-key>.jpeg` or `.png` key with
SSE-S3 encryption, no ACL, and immutable caching. The response is
`{"imageUrl":"https://your-distribution.cloudfront.net/suppliers/<key>.jpeg"}`.
The form saves that URL through the existing supplier API. An image upload
itself does not create a supplier version. Replacing or removing a reference
does not delete the S3 object, so historical versions remain displayable.
Uploads abandoned before a supplier is saved remain in S3; reference-aware
cleanup is deferred. S3 requests and stored objects incur AWS usage charges.

The AWS identity used by the backend needs `s3:PutObject` on
`arn:aws:s3:::foc-supplier-images/suppliers/*`. No bucket management, public ACL,
or object deletion permissions are needed. Keep Block Public Access enabled
and retain the existing CloudFront OAC read policy; ensure it covers the new
`suppliers/` object prefix. With SSE-S3, no KMS permissions are needed. See
[S3 permissions](https://docs.aws.amazon.com/AmazonS3/latest/userguide/using-with-s3-policy-actions.html).

To analyze the SDK calls and produce a baseline for IAM review, run from the
repository root:

```sh
uvx iam-policy-autopilot@latest generate-policies \
  "$PWD/supplier-service/internal/supplier/imageupload/handler.go" \
  --region ap-southeast-1 --service-hints s3 --pretty
```

The generated baseline includes wildcard resources and conditional permissions
for ACLs, tags, Object Lock, Object Lambda, and KMS. Do not attach it unchanged:
review it against this endpoint's use of plain bucket `PutObject` with SSE-S3,
and scope access to the upload prefix above. This command does not apply IAM
changes. Auth0 administrator identity and AWS upload identity are separate.

For local Docker credentials, authenticate the AWS CLI using a named
development profile with temporary credentials (`aws login`, or an existing
IAM Identity Center workflow). AWS CLI browser login also requires the
`SignInLocalDevelopmentAccess` managed policy for a non-root IAM identity.
See [AWS CLI login](https://docs.aws.amazon.com/cli/latest/userguide/cli-configure-sign-in.html).
After authentication, export a temporary credential snapshot into the ignored
file consumed only by supplier-service:

```sh
umask 077
aws configure export-credentials --profile foc-supplier-dev --format env-no-export > supplier-service/.env.aws.local
docker compose up -d --build --force-recreate supplier-service user-frontend
```

Do not print, commit, or share this file. Exported credentials do not refresh
inside the container: when they expire, export them again and recreate
supplier-service. In AWS deployment, omit the local credential file and attach
a scoped IAM role to the service's compute environment; the SDK credential
chain handles it without code changes. No AWS credentials reach the browser.

Validation failures return 400 or 413; missing configuration or an S3 error
(including expired credentials or insufficient access) returns 503 without
provider diagnostics or secrets. The upload has an eight-second dependency
deadline and at most two SDK attempts. There is no database migration for
this feature. Unit tests exercise actual SDK requests against local HTTP
stand-ins; they do not establish live bucket permissions or image delivery.

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
