# User service

PostgreSQL-backed student-account service for the food-delivery platform. Auth0 Universal Login owns registration, credentials, and sessions; this service validates access tokens, provisions local application accounts, and manages profile data. A small browser page exercises the login and protected API.

Business-level deletion remains blocked until coordinated credit/errand checks and Auth0 session revocation are implemented. Role-based access enforcement, administrator/super-administrator management, bootstrap, and audit logging remain future work. The service never receives or stores passwords.

## Structure

```text
cmd/api/main.go          Application entry point
internal/config/        Environment configuration
internal/handler/       HTTP handlers and embedded login test page
internal/service/       Internal student-account operations and future integrations
internal/repository/    Persistence contracts and PostgreSQL adapter
internal/auth/          Auth0 user-profile client
internal/email/         Email sender contract
internal/middleware/    Auth0 JWT authentication
migrations/             Versioned account schema SQL
Dockerfile              Multi-stage image build
go.mod                  Module: user-service
```

Dependencies flow from handlers to business services to repository interfaces. Persistence uses `pgx/v5`. No service database other than the user database is accessed.

## Implemented internal operations

Construct a pool with `repository.Open(ctx, cfg.DatabaseURL)`, close it when finished, and pass `repository.NewPostgres(pool)` to `service.NewUserService(repo, nil)`. The second argument is an optional future credit-balance adapter. These are internal Go APIs, not network endpoints.

| Operation | Current behavior |
| --- | --- |
| `GetProfile` | Require an active account and return public account/profile fields. `credit_balance: null` means the adapter is missing or failed; it never means zero. |
| `UpdateProfile` | Require an active account; update only display name and mobile number. Nil fields are omitted; empty strings explicitly clear fields. |
| `DeleteAccount` | Require confirmation and an active account, then return `ErrUnavailable` without writing anything. |
| Repository `SoftDelete` | Low-level, tested persistence primitive: mark deleted/inactive and exclude the account from normal reads and updates. Does not perform eligibility checks or revoke sessions; never call it directly from a future handler. |

Profile and deletion operations take a trusted authenticated account ID. The future caller must obtain it from real authentication, never a request-supplied identity. There is no authentication substitute, account listing, or target-account override in this slice.

Usernames come from the verified Auth0 profile, with the email local part as fallback. Usernames and emails are case-insensitively unique and remain reserved after soft deletion. Only the exact `u.nus.edu` domain is accepted. Password policy and password reset are configured in the Auth0 database connection.

Display names are limited to 100 characters and mobile numbers to 32, with control characters rejected. Phone ownership/format validation is not implemented. Validation errors contain field names and explanations, not submitted values; conflicts, missing accounts, inactive accounts, and missing dependencies have distinguishable errors.

Soft deletion retains account data and permanently reserves username/email within this slice. Retention, personal-data erasure, safe distributed deletion coordination, and account-wide session invalidation require future work.

## Functional requirement allocation (F1)

These documents allocate the full requirements across folders; their status notes identify the implemented foundation. A requirement can span several folders: handlers translate HTTP requests, services enforce business rules, and adapters provide persistence or external capabilities.

| Folder | Planned responsibility | Requirements |
| --- | --- | --- |
| [cmd/api](cmd/api/README.md) | Startup orchestration and bootstrap lifecycle | F1.6.7, F1.8 |
| [internal/config](internal/config/README.md) | Deployment settings and bootstrap configuration | F1.8.2, F1.8.5–F1.8.6; settings supporting F1.1, F1.3, F1.7, F1.9 |
| [internal/handler](internal/handler/README.md) | HTTP inputs, outputs, and session transport | F1.1–F1.4, F1.6, F1.7, F1.9–F1.10 |
| [internal/service](internal/service/README.md) | Business workflows and cross-service coordination | F1.1–F1.10 |
| [internal/repository](internal/repository/README.md) | User-owned persistence, transactions, and audit records | F1.1–F1.4, F1.6–F1.10 |
| [internal/auth](internal/auth/README.md) | Auth0 identity retrieval | F1.1–F1.3, F1.7–F1.10 |
| [internal/email](internal/email/README.md) | Verification, reset, and activation email delivery | F1.1.4, F1.7, F1.9.3 |
| [internal/middleware](internal/middleware/README.md) | Request authentication and access enforcement | F1.3, F1.6, F1.9.2, F1.10.5 |
| [migrations](migrations/README.md) | Future schema evolution and bootstrap migration coordination | F1.1.1, F1.6.7, F1.8, F1.9–F1.10 |

The service-layer document contains the detailed requirement breakdown. Frontend forms and confirmation screens belong to the consuming application; this service must still validate submitted inputs and enforce the corresponding rules. Errand, credit, supplier, and dispute behavior stays with the owning services.

## Ownership boundaries

The user service owns:

- User identity
- Profile information
- Account roles
- Account activation and deactivation
- Audit records related to user-management actions

The user service does not own:

- Authentication credentials or password hashes
- Auth0 sessions
- Errands
- Courier assignment
- Order state
- Credit reservations
- Supplier records

Access these domains through service APIs or events in the future, never by directly querying another service's database.

A normal **USER** account can act as both requester and courier. Requester and courier are business activities, not separate account roles.

## Development

Use Go 1.26.7 or newer, matching the existing module's minimum version. The module is already initialized as `user-service` (the result of `go mod init user-service`); do not reinitialize it.

Run from `user-service/`:

```sh
gofmt -w cmd internal
go vet ./...
go test ./...
go build ./...
go run ./cmd/api
```

Copy `.env.example` to `.env`, fill in the values, and run `go run ./cmd/api`. The entry point connects to PostgreSQL and listens on `HTTP_ADDRESS` (default `:8080`). `repository.Open` parses the URL and pings PostgreSQL with the caller's context; its errors omit credentials and driver details. Do not log configuration, bearer tokens, or credential inputs.

Create an Auth0 API named `FoC User Service API` with identifier `https://api.foc.local/user-service` and RS256 signing. Create a Single Page Application named `FoC Login Test`, then set its Allowed Callback URLs, Allowed Logout URLs, and Allowed Web Origins to `http://localhost:8080`. Put its tenant domain and client ID in `.env`; no client secret is used by the SPA.

Set the API's **Token Expiration** to `600` seconds. Logout clears the SPA's in-memory state and Auth0 browser session, but an issued stateless JWT remains valid until it expires. The service deliberately has no local session table or token denylist.

The local endpoints are:

- `GET /` — embedded Auth0 login test page.
- `GET /health` and `GET /api/auth/config` — public service/configuration endpoints.
- `POST /api/auth/provision` — requires an access token and creates an active local USER from a matching, verified NUS Auth0 profile.
- `POST /api/auth/logout` — validates the current access token before the SPA ends its Auth0 browser session; returns `204 No Content`.
- `GET /api/private` — requires a valid Auth0 access token and a provisioned, active local account.

Automatic provisioning never links an existing local account by email. A username or email conflict requires a future explicit account-linking workflow. The Auth0 nickname becomes the username; a missing or invalid nickname falls back to the email local part.

Use a dedicated user-service database on PostgreSQL 16 or newer. Set `DATABASE_URL` through your environment/secret manager, then apply the initial migration once:

```sh
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations/000001_accounts.up.sql
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations/000002_auth0_identities.up.sql
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations/000003_auth0_only.up.sql
```

Migrations are explicit, transactional SQL files; there is no automatic runner or bootstrap. See [migration instructions](migrations/README.md) for rollback behavior.

### PostgreSQL integration tests

Set `TEST_DATABASE_URL` to a test database whose user may create schemas, then run:

```sh
go test -count=1 ./...
```

Without `TEST_DATABASE_URL`, PostgreSQL integration tests are skipped explicitly; unit tests still run. Each integration test creates and removes a randomly named isolated schema. Tests never fall back to `DATABASE_URL`, reset an existing schema/database, or require pre-applied migrations. Integration coverage includes concurrent uniqueness, Auth0 provisioning, CRUD, guarded profile updates, blocked business deletion, and migration rollback/reapply.

Build the container with `docker build -t user-service .`. Supply all variables documented in `.env.example` when running it; the environment file itself is not copied into the image.
