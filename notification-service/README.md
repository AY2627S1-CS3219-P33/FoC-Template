<!--
AI Assistance Disclosure:
Tool: Claude, date: 2026-11-11
Scope: service project documentation for notification-service.
Author review: I validated correctness, edited for style, and added boundary checks.
-->

# notification-service

Per-user notifications about errands. The service subscribes to the errand
events on the shared RabbitMQ topic exchange, turns each one into a notification
for the users it concerns, stores them, and serves each user their own over an
Auth0-protected HTTP API. It consumes only; it publishes nothing.

## What it does

- Binds a single quorum queue (`notification-service.errands`) to `errand.#` on
  the `foc.events` exchange and processes each event once (deduplicated by the
  event's `messageId`, per recipient).
- Derives recipients from the event's `requesterId`, `courierId` and `actorId`,
  and never notifies a party about an action it took itself.
- Exposes the caller's own notifications; a user can neither create a
  notification nor read anyone else's.

See [openapi.yaml](openapi.yaml) for the HTTP contract and
[docs/contracts.md](docs/contracts.md) for the event-to-notification mapping.
The messaging contract it implements is [docs/asyncapi.yaml](../docs/asyncapi.yaml).

## HTTP API

| Method & path | Caller | Permission |
| --- | --- | --- |
| `GET /v1/notifications` | user | `notifications:read:own` |
| `GET /v1/notifications/unread-count` | user | `notifications:read:own` |
| `POST /v1/notifications/{id}/read` | user | `notifications:write:own` |
| `POST /v1/notifications/read-all` | user | `notifications:write:own` |
| `GET /readyz` | — | none |

Listing is keyset-paginated newest-first by `(createdAt, id)`; pass the
`nextCursor` from one page to get the next.

## Layout

```
cmd/notification-service   entrypoint
internal/app               composition root + server lifecycle
internal/config            environment configuration
internal/auth              Auth0 RS256 verification + permission middleware
internal/notifmsg          AMQP consumer, topology, event-to-notification mapping
internal/notification      notification aggregate + use cases
internal/database/notifrepo PostgreSQL store
internal/notificationapi   HTTP handlers
internal/readiness         readiness probe
migrations                 goose SQL migrations (applied out of band)
```

## Running locally

The service needs PostgreSQL and RabbitMQ. The root
[compose.yaml](../compose.yaml) wires a `notification-db`, applies the
migrations with a `notification-migrate` job, and runs the service on
`127.0.0.1:3007` against the shared `queue-service` broker:

```bash
docker compose up notification-service
```

To run it directly, copy `.env.example` to `.env`, fill in `DATABASE_URL`,
`AMQP_URL` and `AUTH0_ISSUER`, apply the migrations with goose, then
`go run ./cmd/notification-service`.

## Configuration

See [.env.example](.env.example). The service migrates nothing itself (the
runtime role has no DDL rights) and holds no secrets beyond the connection
URLs, which are never logged.
