# Chat service

Lets the **requester** and the **assigned courier** of an errand message each other in real time. A room opens when a courier accepts an errand, becomes read-only 24 hours after the errand ends, and is kept for admin export. The design, decisions and build order are in [implementation-plan.md](implementation-plan.md).

**Status:** phases 1 (skeleton) and 2 (database) are done. The service starts, validates its configuration, and serves `GET /readyz`. The database schema exists, and every query the service will need is written and tested. There are no user-facing routes yet: REST routes come in phase 3, room creation from errand events in phase 4, and WebSockets in phase 5.

## Contents

- [What each phase built, and why](#what-each-phase-built-and-why)
- [Technology](#technology)
- [Project structure](#project-structure)
- [Running](#running)
- [Working with the database](#working-with-the-database)
- [Environment variables](#environment-variables)
- [Tests](#tests)

## What each phase built, and why

This section is for readers new to backend development. It explains what each part is for and what would go wrong without it.

### Phase 1: skeleton

| Part | What it does | Why it matters |
|---|---|---|
| Configuration ([internal/config](internal/config/config.go)) | Reads every setting from environment variables at startup, checks them all, and reports every problem in one error. | A wrong setting stops the service at startup with a clear message, not hours later on the first request that needs it. Errors never show values, because URLs contain passwords. |
| Readiness check (`GET /readyz`) | Answers `200` only when the service can do its job, `503` otherwise. | Docker Compose and the AWS load balancer use it to decide whether to send traffic to this copy of the service. |
| Dockerfile and `compose.yaml` | Build the service into a small container and start it with its database. | Every teammate, CI and AWS run the same thing, so "works on my machine" problems go away. |

### Phase 2: database

#### 1. A full migration ([migrations/00001_create_chat_schema.sql](migrations/00001_create_chat_schema.sql))

**What a migration is.** A migration is a numbered SQL file that changes the database's structure: it creates tables, adds columns, and so on. A tool called **goose** applies migrations in number order and records which ones it has applied in a table called `goose_db_version`. Each file has two halves: `-- +goose Up` (make the change) and `-- +goose Down` (undo it).

**Why use migrations instead of creating tables by hand?** Every database has to end up with exactly the same structure: your laptop, your teammates' laptops, CI and the AWS database. If people typed `CREATE TABLE` by hand, the databases would slowly drift apart and bugs would appear on one machine only. With migrations, the files in Git *are* the structure, and goose makes any database match them.

**Why "full"?** This first migration creates *every* table and column the finished service needs, even the ones only used in later phases:

- `chat_rooms`: one row per (errand, courier) pair. It includes the closing columns (`ended_at`, `end_reason`, `closes_at`), although the closing logic is only built in phase 7.
- `chat_messages`: every message, never deleted.
- `errand_endings`: remembers that an errand completed or was refunded, for the case where that event arrives *before* the room is created (RabbitMQ may deliver events out of order).
- `chat_log_exports`: an audit trail of which admin downloaded which chat log.

Designing the whole schema now has three benefits:

1. **No second migration later.** Changing a table that already holds real data is the riskiest kind of database change: you have to think about existing rows, locks on large tables, and deploying old and new code side by side. Getting the structure right once avoids that work.
2. **The design is checked early.** Writing every table forced us to think through the tricky cases (duplicate events, events in the wrong order, a courier withdrawing) before any code depends on the structure.
3. **Later phases only add code.** Phases 3 to 8 write Go, not SQL schema changes.

**The database enforces the rules too.** The tables have *constraints*: rules PostgreSQL itself refuses to break, whatever the code does. For example:

| Constraint | Rule | What it protects against |
|---|---|---|
| `chat_rooms_one_per_assignment` | One room per (errand, courier) | A repeated `errand.accepted` event creating a second room |
| `chat_rooms_distinct_people` | Requester ≠ courier | Someone chatting with themselves |
| `chat_rooms_end_fields` | `ended_at`, `end_reason` and `closes_at` are all set or all empty, and a room cannot close before it ends | Half-ended rooms that would never close |
| `chat_messages_idempotent` | One message per (room, `client_msg_id`) | A browser retrying a send and posting the message twice |
| `chat_messages_body_length` | 1 to 2000 characters | Empty or huge messages |
| Foreign keys (`REFERENCES chat_rooms`) | Messages and exports belong to an existing room, and a room with messages cannot be deleted | Orphan messages, and lost chat history |

Even if a future bug skipped a check in Go, the database would still refuse bad data. The tests found a real gap this way: the planned `chat_rooms_end_fields` rule allowed a room with an end time but no closing time, because in SQL a comparison with an empty (`NULL`) value is "unknown", and a constraint only rejects "false". The rule now also requires `closes_at IS NOT NULL`.

**Why can it go both up and down?** The `Down` half undoes the `Up` half. A test applies the migration, undoes it, and applies it again, so if a deployment goes wrong the change can be rolled back cleanly.

**Who applies it?** A separate one-shot container, `chat-migrate`, runs goose before the service starts. The service itself never changes the database structure. On AWS the service's database user will only be allowed to read and write rows, so a bug or an attacker cannot drop tables through it.

#### 2. Queries and sqlc ([queries/chat.sql](queries/chat.sql), [sqlc.yaml](sqlc.yaml))

Every SQL statement the service runs lives in one file, `queries/chat.sql`. A tool called **sqlc** reads that file together with the migrations and *generates* Go functions for it (in `internal/database/chatgen`, not committed to Git).

**Why?** If you write SQL as strings inside Go code, a typo in a column name, or a column renamed in a migration, is only discovered when that line runs, possibly in production. sqlc checks every query against the real schema when the code is generated, and gives you typed Go functions, so mistakes are found at build time. The Docker build and CI regenerate the code, so they always match the schema.

Some queries are written so that **repeating them is harmless** (this property is called *idempotence*). RabbitMQ may deliver the same event twice, and browsers retry sends when the network drops:

- `UpsertRoomOnAccept` creates a room, or does nothing if it already exists.
- `InsertMessage` ignores a second message with the same `client_msg_id`.
- `EndRoom` only ever moves a room's end time *earlier*, so applying the same event twice, or an older event late, changes nothing.

`LockErrand` takes a PostgreSQL *advisory lock* per errand, so two events for the same errand are applied one after the other, even when several copies of the service run.

#### 3. The store ([internal/store](internal/store/))

The store is the **only** package that talks to the database. The rest of the service calls methods like `GetRoom` or `InsertMessage` and gets back plain Go types from [internal/chat](internal/chat/types.go) (`Room`, `Message`), never database types.

**Why a separate layer?**

- **One place for SQL.** When a query needs to change, there is exactly one place to look.
- **Business rules stay testable.** The rules in phase 3 (who may read, who may send) can be tested with a fake store in memory, with no database.
- **Errors are translated.** A database error like "violates check constraint chat_messages_body_length" becomes `chat.ErrInvalidMessage`, which the API can turn into a clear `400` response. Failures that a retry might fix, such as a lost connection, become `chat.ErrUnavailable`, so the event consumer knows to try again later. Raw database errors never leave the store, because they can contain message text or connection details.
- **Transactions.** `WithTx` runs several queries as one unit: either all of them are saved, or none are. This matters when, for example, a room is created and immediately ended in one event.

The store also provides the schema check used by `/readyz`: the service reports *not ready* until the database has every migration this build expects. If someone forgets to run a migration, the problem shows up immediately as an unhealthy service instead of as errors on the first request.

`chat.Room.Status(now)` works out whether a room is `OPEN`, `CLOSING` or `CLOSED` by comparing `closes_at` with the current time. The status is never stored, so no background job is needed to close rooms, and every copy of the service always agrees.

#### 4. Integration tests

*Unit* tests check code on its own. *Integration* tests check it against a real PostgreSQL, because many of the important rules live in the database: constraints, `ON CONFLICT`, locks. These tests:

- apply the migration, undo it, and apply it again;
- try to break each constraint and check PostgreSQL refuses;
- run every query in the store, including the tricky cases: duplicate events, `errand.withdrawn` before `errand.accepted`, a withdrawn courier's room keeping its earlier end time when the errand later completes, and a second transaction waiting for another's errand lock.

Each test gets its **own fresh schema** (a separate namespace inside the test database), so tests never see each other's data, and the schema is deleted afterwards.

## Technology

Go, `net/http`, `pgx/v5` (PostgreSQL driver), `goose` (migrations), `sqlc` (typed SQL), `log/slog` JSON logs. Planned: Auth0 JWTs (phase 3), `amqp091-go` (phase 4), `github.com/coder/websocket` (phase 5).

## Project structure

```
cmd/chat-service/         entry point
internal/app/             composition root and HTTP server lifecycle
internal/config/          environment configuration
internal/chat/            business types (Room, Message, Status); rules from phase 3
internal/store/           the only package that runs SQL: queries, transactions, error mapping
internal/database/chatgen/  sqlc output (generated, git-ignored)
internal/httpapi/         router (REST routes arrive in phase 3)
internal/readiness/       GET /readyz
internal/testdb/          per-test migrated PostgreSQL schema
internal/logging/         slog setup
migrations/               goose SQL migrations (+ embedded for tests and /readyz)
queries/chat.sql          every SQL statement, input for sqlc
sqlc.yaml                 sqlc settings
```

## Running

With the whole stack, from the repository root:

```sh
docker compose up --build chat-service
curl localhost:3005/readyz        # {"status":"ready","dependencies":{"postgres":"ok"}}
docker compose ps chat-service    # STATUS shows (healthy)
```

This starts three containers in order: `chat-db` (PostgreSQL), `chat-migrate` (applies migrations, then exits), and `chat-service`. `chat-service/.env` is optional for now: Compose supplies local defaults, including a placeholder Auth0 issuer. Copy `.env.example` to `.env` to set the real tenant or other options.

On the host (needs a reachable, migrated PostgreSQL):

```sh
cp .env.example .env              # set DATABASE_URL and AUTH0_ISSUER
go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate
go run ./cmd/chat-service
```

`/readyz` returns `503 {"status":"not_ready"}` when PostgreSQL does not answer or its schema is older than this build. From phase 4 it also requires the RabbitMQ consumer to be subscribed.

## Working with the database

**Adding a migration.** Never edit a migration that has been applied anywhere shared; add the next numbered file instead (`00002_<what>.sql`, with `-- +goose Up` and `-- +goose Down`). Then:

```sh
go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate   # regenerate Go code
docker compose run --rm chat-migrate                         # from the repo root
```

**Changing a query.** Edit `queries/chat.sql`, run `sqlc generate`, then add or update the store method that calls it.

**Looking at the tables.** `chat-db` sits on a private Docker network with no port open to your machine, like the other services' databases. To look inside:

- From the terminal: `docker compose exec chat-db psql -U chat_service -d chat_service`, then `\dt` lists the tables.
- With DBeaver or another GUI: migrate the test database and connect to it at `127.0.0.1:5435`, database `chat_test`, user `chat_test`, no password:
  ```sh
  docker compose -f compose.test.yaml up -d
  GOOSE_DRIVER=postgres GOOSE_DBSTRING='postgres://chat_test@127.0.0.1:5435/chat_test?sslmode=disable' \
    go run github.com/pressly/goose/v3/cmd/goose@v3.29.0 -dir migrations up
  ```
  The tables are in the `public` schema. Schemas named `chat_test_<random>` that appear while tests run are deleted when the tests finish.

## Environment variables

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `DATABASE_URL` | Yes | | PostgreSQL URL. On RDS add `sslmode=verify-full&sslrootcert=/etc/ssl/certs/rds-global-bundle.pem` |
| `TEST_DATABASE_URL` | When `APP_ENV=test` | | Test database URL, used instead of `DATABASE_URL` |
| `AMQP_URL` | Yes | | RabbitMQ URL (`amqp://` or `amqps://`) |
| `AUTH0_ISSUER` | Yes | | Auth0 tenant, e.g. `https://login.bestfoc.app/` |
| `AUTH0_AUDIENCE` | No | `https://api.foc.local/chat-service` | Auth0 API identifier |
| `WS_ALLOWED_ORIGINS` | Yes | | Comma-separated host patterns allowed to open a WebSocket, e.g. `localhost:8081,localhost:5173`; production `app.bestfoc.app` |
| `APP_ENV` | No | `development` | `development`, `test` or `production` |
| `HTTP_HOST` / `HTTP_PORT` | No | `0.0.0.0` / `3005` | Listen address |
| `LOG_LEVEL` | No | `info` | `debug`, `info`, `warn`, `error` |
| `STARTUP_TIMEOUT` / `SHUTDOWN_TIMEOUT` | No | `30s` / `10s` | |
| `DATABASE_POOL_MIN` / `DATABASE_POOL_MAX` | No | `1` / `10` | |
| `CHAT_CLOSE_DELAY` | No | `24h` | How long a room stays writable after its errand ends. `1m` for manual testing |
| `CHAT_MAX_MESSAGE_LENGTH` | No | `2000` | 1 to 2000 (the database limit) |
| `CHAT_SEND_RATE` / `CHAT_SEND_BURST` | No | `5` / `10` | Per-connection send limit: messages per second, and burst |
| `WS_AUTH_TIMEOUT` | No | `5s` | Time allowed for the first `auth` frame |
| `WS_PING_INTERVAL` | No | `25s` | Must be under 60 s, the ALB idle timeout |
| `WS_MAX_CONNECTIONS_PER_USER` | No | `5` | 1 to 100 |
| `REDIS_URL` | No | empty | Empty: in-process broadcaster. Set when running more than one copy |

Configuration errors list every problem at once and never include a value.

## Tests

```sh
go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate   # once, and after changing queries or migrations
gofmt -l . && go vet ./... && go test ./... && go build ./...
```

Without a database, the tests that need PostgreSQL skip themselves. To run them too:

```sh
docker compose -f compose.test.yaml up -d
TEST_DATABASE_URL='postgres://chat_test@127.0.0.1:5435/chat_test?sslmode=disable' go test -p 1 ./...
```

CI runs all of them against its own PostgreSQL.
