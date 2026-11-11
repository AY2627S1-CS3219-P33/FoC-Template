<!--
AI Assistance Disclosure:
Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-10
Scope: Documentation writing and formatting.
Author review: Reviewed, validated, and tested by the authors.
-->

# Chat service agent notes

## Scope
This service owns chat between an errand's requester and its assigned courier: rooms, messages, and live delivery over WebSocket. Errand, credit and user logic must not live here. Never query another service's database; room lifecycle comes only from order-service's errand events on RabbitMQ. The design and build order are in `implementation-plan.md`; follow it and keep it up to date when a decision changes.

## Rules that must not drift
- One room per `(errandId, courierId)`. A new courier gets a new, empty room and never sees an earlier courier's messages.
- Room status (`OPEN`, `CLOSING`, `CLOSED`) is calculated from `closes_at` and the server clock, never stored and never closed by a background job. `CLOSED` is read-only; rooms and messages are never deleted.
- Outsiders get `404 ROOM_NOT_FOUND`, never `403`, so they cannot tell a room exists.
- Save, commit, then publish to the broadcaster. Never push a message that is not in PostgreSQL.
- No business state in memory: rooms, membership and messages always come from PostgreSQL. Only open WebSocket connections live in memory (`hub`). This keeps the later Redis switch inside `broadcast`.
- Dependency direction: `httpapi`, `ws`, `events` → `chat` → `store`, `broadcast` → `hub`. `chat` never imports `httpapi` or `ws`.
- Event handlers are idempotent and order-independent (at-least-once, may reorder): one transaction per event, starting with `LockErrand`; `EndRoom` only moves an end time earlier.

## Conventions
Follow `order-service` and `credit-service`: env-only config in `internal/config` (report every problem at once, never print a value), composition in `internal/app`, goose migrations in `migrations/` applied by the `chat-migrate` job (the service never runs DDL), `log/slog` JSON logs, no secrets, tokens or connection URLs in errors or logs. Never log message bodies. Every new migration is a new file; never edit an applied one.

## Database
All SQL lives in `queries/chat.sql`; run `go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate` after changing it or a migration (`internal/database/chatgen` is generated and git-ignored). Only `internal/store` imports `chatgen` or pgx types; it returns `chat` types and translates database errors into `chat` errors (`ErrInvalidMessage`, `ErrInvalidRoom`, `ErrRoomNotFound`, or `ErrUnavailable` when a retry may help). Keep business rules out of the store. Group writes that must succeed together in `Store.WithTx`; `LockErrand` only works inside it. Database tests use `internal/testdb`, which gives each test its own migrated schema.

## Rules and HTTP
Every rule (participant check, room status, message validation, paging) lives in `chat.Service`; handlers only parse requests, call it with the token's subject, and map its errors. Errors use `internal/apperror`: `{"code":"UPPER_SNAKE","message":"..."}` like the other Go services; add a code there and to `openapi.yaml` together. Never put a database error, URL or message text in an error response or a log line. Update `openapi.yaml` with every route change; CI lints it. Rule tests use `chat/chattest` (in-memory store, fake clock); handler tests use a fake `auth.Port`.

## Events
`internal/events` owns `chat-service.errand-events` and its `.dlq`; it declares `foc.events` with order-service's exact arguments. Add a routing key to `Bindings()`, a decoder in `message.go`, a case in `Handler.Handle` and a `chat.Lifecycle` method together, and list chat-service as a receiver in `docs/asyncapi.yaml`. Ack only after the transaction commits. `chat.ErrUnavailable` means retry until it works (never dead-letter a temporary failure: order-service does not resend events); `chat.ErrInvalidEvent` means dead-letter; anything else is retried `MaxAttempts` times. RabbitMQ 4 does not count explicit requeues towards `x-delivery-limit`. Never log user IDs or message bodies from events; log the message ID and errand ID. Consumer tests need `TEST_AMQP_URL` pointing at the throwaway broker in `compose.test.yaml`, never `queue-service`.

## Live delivery
Frames, close codes and the Room/Message JSON live only in `internal/protocol`; update `docs/websocket-protocol.md` with any change. `chat` pushes through `chat.Notifier` only after a commit, and never for a retry. Only `broadcast` and `hub` know how delivery works; address envelopes to users, never connections. The hub must never block (`Deliver` drops slow clients). Keep business state out of `hub` and `ws`. A WebSocket token arrives in the first frame, never the URL; the connection closes at the token's expiry.

## Authentication
`internal/auth` is copied from order-service; keep the two in step. Identity and permissions come only from the verified Auth0 token, never from a request body or WebSocket frame field. `chats:use` is the only permission, for every chat route and the WebSocket; machine-to-machine tokens are refused on all routes. WebSocket tokens arrive in the first `auth` frame, never in the URL.

## Verify
```sh
gofmt -l . && go vet ./... && go test ./... && go build ./...
```
Tests that need PostgreSQL or RabbitMQ skip without `TEST_DATABASE_URL` / `TEST_AMQP_URL` (see `compose.test.yaml`); run them with `go test -p 1 ./...`.
