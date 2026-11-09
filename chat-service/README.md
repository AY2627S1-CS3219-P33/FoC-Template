# Chat service

Lets the **requester** and the **assigned courier** of an errand message each other in real time. A room opens when a courier accepts an errand, becomes read-only 24 hours after the errand ends, and is kept for admin export. The design, decisions and build order are in [implementation-plan.md](implementation-plan.md).

**Status:** phase 1 (skeleton). The service starts, validates its configuration, connects to PostgreSQL and serves `GET /readyz`. Rooms, messages, WebSockets and the event consumer come in later phases.

## Technology

Go, `net/http`, `pgx/v5`, `log/slog` JSON logs. Planned: `goose` and `sqlc` (phase 2), Auth0 JWTs (phase 3), `amqp091-go` (phase 4), `github.com/coder/websocket` (phase 5).

## Project structure

```
cmd/chat-service/     entry point
internal/app/         composition root and HTTP server lifecycle
internal/config/      environment configuration
internal/httpapi/     router (REST routes arrive in phase 3)
internal/readiness/   GET /readyz
internal/store/       PostgreSQL pool (repository arrives in phase 2)
internal/logging/     slog setup
```

## Running

With the whole stack, from the repository root:

```sh
docker compose up --build chat-service
curl localhost:3005/readyz        # {"status":"ready","dependencies":{"postgres":"ok"}}
docker compose ps chat-service    # STATUS shows (healthy)
```

`chat-service/.env` is optional for now: Compose supplies local defaults, including a placeholder Auth0 issuer. Copy `.env.example` to `.env` to set the real tenant or other options.

On the host (needs a reachable PostgreSQL):

```sh
cp .env.example .env              # set DATABASE_URL and AUTH0_ISSUER
go run ./cmd/chat-service
```

`/readyz` returns `503 {"status":"not_ready"}` when PostgreSQL does not answer. From phase 4 it also requires the RabbitMQ consumer to be subscribed.

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
gofmt -l . && go vet ./... && go test ./... && go build ./...
```

Tests that need PostgreSQL skip themselves unless `TEST_DATABASE_URL` is set:

```sh
docker compose -f compose.test.yaml up -d
TEST_DATABASE_URL='postgres://chat_test@127.0.0.1:5435/chat_test?sslmode=disable' go test -p 1 ./...
```
