# notification-service — notes for agents

A consumer-only Go microservice: it reads errand events off RabbitMQ, stores
per-user notifications in PostgreSQL, and serves each user their own over an
Auth0-protected HTTP API. It publishes nothing.

## Boundaries

- **Identity comes only from the verified token.** HTTP routes resolve the
  recipient from the Auth0 subject; never take a user id from the request.
- **Recipients come only from the event payload.** `requesterId`, `courierId`,
  `actorId`. Contact names and mobile numbers are never published, so never
  read or store them.
- **Consume only.** No publisher, no outbox. Do not add a command exchange or a
  `notification.requested` command — the contract has none.
- **Idempotent writes.** Dedup is `(messageId, recipient)`; keep the unique
  constraint and `ON CONFLICT DO NOTHING`. Ack only after rows commit.

## Where things live

- Event-to-notification mapping: [internal/notifmsg/handler.go](internal/notifmsg/handler.go)
  (`classify`). Add a new event by adding its routing key and a row to the
  `lifecycle` table or a case in `classify`.
- AMQP plumbing (reconnect, retry, dead-letter): `internal/notifmsg/consumer.go`
  and `topology.go`, adapted from credit-service.
- Auth (`internal/auth`), config, logging, readiness and the DB pool are adapted
  from credit-service; keep them in step when that service's copies change.

## Conventions

- Run `gofmt -w`, `go vet ./...` and `go test ./...` before committing.
- The service applies no migrations (the runtime role has no DDL rights); add
  goose migrations under `migrations/` and let the migrate job apply them.
- Errors returned to callers use the stable codes in
  [internal/apperror/error.go](internal/apperror/error.go); never leak
  connection strings or provider error text.
