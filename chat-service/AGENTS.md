# Chat service agent notes

## Scope
This service owns chat between an errand's requester and its assigned courier: rooms, messages, live delivery over WebSocket, and admin log export. Errand, credit and user logic must not live here. Never query another service's database; room lifecycle comes only from order-service's errand events on RabbitMQ. The design and build order are in `implementation-plan.md`; follow it and keep it up to date when a decision changes.

## Rules that must not drift
- One room per `(errandId, courierId)`. A new courier gets a new, empty room and never sees an earlier courier's messages.
- Room status (`OPEN`, `CLOSING`, `CLOSED`) is calculated from `closes_at` and the server clock, never stored and never closed by a background job. `CLOSED` is read-only; rooms and messages are never deleted.
- Outsiders get `404 room_not_found`, never `403`, so they cannot tell a room exists.
- Save, commit, then publish to the broadcaster. Never push a message that is not in PostgreSQL.
- No business state in memory: rooms, membership and messages always come from PostgreSQL. Only open WebSocket connections live in memory (`hub`). This keeps the later Redis switch inside `broadcast`.
- Dependency direction: `httpapi`, `ws`, `events` → `chat` → `store`, `broadcast` → `hub`. `chat` never imports `httpapi` or `ws`.
- Event handlers are idempotent and order-independent (at-least-once, may reorder): one transaction per event, starting with `LockErrand`; `EndRoom` only moves an end time earlier.

## Conventions
Follow `order-service` and `credit-service`: env-only config in `internal/config` (report every problem at once, never print a value), composition in `internal/app`, goose migrations in `migrations/` applied by the `chat-migrate` job (the service never runs DDL), `log/slog` JSON logs, no secrets, tokens or connection URLs in errors or logs. Never log message bodies. Every new migration is a new file; never edit an applied one.

## Database
All SQL lives in `queries/chat.sql`; run `go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate` after changing it or a migration (`internal/database/chatgen` is generated and git-ignored). Only `internal/store` imports `chatgen` or pgx types; it returns `chat` types and translates database errors into `chat` errors (`ErrInvalidMessage`, `ErrInvalidRoom`, `ErrRoomNotFound`, or `ErrUnavailable` when a retry may help). Keep business rules out of the store. Group writes that must succeed together in `Store.WithTx`; `LockErrand` only works inside it. Database tests use `internal/testdb`, which gives each test its own migrated schema.

## Authentication
Identity and permissions come only from the verified Auth0 token, never from a request body or WebSocket frame field. `chats:use` for participant routes and the WebSocket, `chats:export` for `/admin/*`. WebSocket tokens arrive in the first `auth` frame, never in the URL.

## Verify
```sh
gofmt -l . && go vet ./... && go test ./... && go build ./...
```
Tests that need PostgreSQL skip without `TEST_DATABASE_URL` (see `compose.test.yaml`); run them with `go test -p 1 ./...`.
