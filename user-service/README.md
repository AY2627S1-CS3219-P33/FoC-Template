# User service

PostgreSQL-backed student-account service for the food-delivery platform. Registration validation, secure password hashing, account persistence, internal profile operations, Auth0 login, and first-login account provisioning are implemented. A small browser page exercises Auth0 Universal Login and the protected API.

Business-level deletion remains blocked until coordinated credit/errand checks and session revocation are implemented. Password reset, role-based access enforcement, administrator/super-administrator management, bootstrap, and audit logging remain future work. Auth0 owns credentials and sessions for externally provisioned accounts; the service validates access tokens and stores no Auth0 password.

## Structure

```text
cmd/api/main.go          Application entry point
internal/config/        Environment configuration
internal/handler/       HTTP handlers and embedded login test page
internal/service/       Internal student-account operations and future integrations
internal/repository/    Persistence contracts and PostgreSQL adapter
internal/auth/          Argon2id password hashing; future session/token contracts
internal/email/         Email sender contract
internal/middleware/    Auth0 JWT authentication
migrations/             Versioned account schema SQL
Dockerfile              Multi-stage image build
go.mod                  Module: user-service
```

Dependencies flow from future handlers to business services to repository interfaces. Persistence uses `pgx/v5`; password hashing uses `golang.org/x/crypto/argon2`. No service database other than the user database is accessed.

## Implemented internal operations

Construct a pool with `repository.Open(ctx, cfg.DatabaseURL)`, close it when finished, and pass `repository.NewPostgres(pool)` to `service.NewUserService(repo, nil)`. The second argument is an optional future credit-balance adapter. These are internal Go APIs, not network endpoints.

| Operation | Current behavior |
| --- | --- |
| `Register` | Validate username, NUS email, password policy, and exact confirmation; create an inactive USER with a salted password hash. Does not send email or activate the account. |
| `GetProfile` | Require an active account and return public account/profile fields. `credit_balance: null` means the adapter is missing or failed; it never means zero. |
| `UpdateProfile` | Require an active account; update only display name and mobile number. Nil fields are omitted; empty strings explicitly clear fields. |
| `DeleteAccount` | Require confirmation and an active account, then return `ErrUnavailable` without writing anything. |
| Repository `SoftDelete` | Low-level, tested persistence primitive: mark deleted/inactive and exclude the account from normal reads and updates. Does not perform eligibility checks or revoke sessions; never call it directly from a future handler. |

Profile and deletion operations take a trusted authenticated account ID. The future caller must obtain it from real authentication, never a request-supplied identity. There is no authentication substitute, account listing, or target-account override in this slice.

Usernames are trimmed, retain their display casing, and must contain 1–64 characters without control characters. Usernames are display labels and are not unique. Emails are trimmed, lowercased, limited to 254 bytes, and remain unique even after soft deletion. Only `nus.edu.sg` and valid subdomains are accepted; `notnus.edu.sg` is rejected.

Passwords require at least eight Unicode characters with uppercase, lowercase, a digit, and a punctuation/symbol character; whitespace does not satisfy the symbol requirement. They are never trimmed or normalized. A 1024-byte limit bounds hashing input. Argon2id uses a random 16-byte salt, 19 MiB memory, two iterations, one lane, and a 32-byte key, following [OWASP password-storage guidance](https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html). Verification accepts only the implemented parameter set and compares derived keys in constant time.

Display names are limited to 100 characters and mobile numbers to 32, with control characters rejected. Phone ownership/format validation is not implemented. Public profiles never contain password hashes. Validation errors contain field names and explanations, not submitted values; conflicts, missing accounts, inactive accounts, and missing dependencies have distinguishable errors.

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
| [internal/auth](internal/auth/README.md) | Password, session, and token security | F1.1–F1.3, F1.7–F1.10 |
| [internal/email](internal/email/README.md) | Verification, reset, and activation email delivery | F1.1.4, F1.7, F1.9.3 |
| [internal/middleware](internal/middleware/README.md) | Request authentication and access enforcement | F1.3, F1.6, F1.9.2, F1.10.5 |
| [migrations](migrations/README.md) | Future schema evolution and bootstrap migration coordination | F1.1.1, F1.6.7, F1.8, F1.9–F1.10 |

The service-layer document contains the detailed requirement breakdown. Frontend forms and confirmation screens belong to the consuming application; this service must still validate submitted inputs and enforce the corresponding rules. Errand, credit, supplier, and dispute behavior stays with the owning services.

## Ownership boundaries

The user service owns:

- User identity
- Credentials and password hashes
- Sessions
- Profile information
- Account roles
- Account activation and deactivation
- Audit records related to user-management actions

The user service does not own:

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

The local endpoints are:

- `GET /` — embedded Auth0 login test page.
- `GET /health` and `GET /api/auth/config` — public service/configuration endpoints.
- `POST /api/auth/provision` — requires an access token and creates an active local USER from a matching, verified NUS Auth0 profile.
- `GET /api/private` — requires a valid Auth0 access token.

Automatic provisioning never links an existing local account by email. A conflict requires a future explicit account-linking workflow. The Auth0 nickname becomes the non-unique username; a missing or invalid nickname falls back to the email local part.

Use a dedicated user-service database on PostgreSQL 16 or newer. Set `DATABASE_URL` through your environment/secret manager, then apply the initial migration once:

```sh
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations/000001_accounts.up.sql
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations/000002_auth0_identities.up.sql
```

Migrations are explicit, transactional SQL files; there is no automatic runner or bootstrap. See [migration instructions](migrations/README.md) for rollback behavior.

### PostgreSQL integration tests

Set `TEST_DATABASE_URL` to a test database whose user may create schemas, then run:

```sh
go test -count=1 ./...
```

Without `TEST_DATABASE_URL`, PostgreSQL integration tests are skipped explicitly; unit tests still run. Each integration test creates and removes a randomly named isolated schema. Tests never fall back to `DATABASE_URL`, reset an existing schema/database, or require pre-applied migrations. Fixtures activate accounts only inside the isolated test schema; there is no production activation bypass. Integration coverage includes concurrent uniqueness, CRUD, guarded profile updates, blocked business deletion, and migration rollback/reapply.

Build the container with `docker build -t user-service .`. Supply all variables documented in `.env.example` when running it; the environment file itself is not copied into the image.
