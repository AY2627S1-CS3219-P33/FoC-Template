<!--
AI Assistance Disclosure:
Tool: Claude, date: 2026-11-11
Scope: service development instructions for notification-service.
Author review: I validated correctness, edited for style, and added boundary checks.
-->

# notification-service — notes for agents

A consumer-only Go microservice: it reads errand events off RabbitMQ, stores
per-user notifications in PostgreSQL, and serves each user their own over an
Auth0-protected HTTP API. The unread count is pushed to clients over
Server-Sent Events (`GET /v1/notifications/stream`) rather than polled. It
publishes nothing to the broker.

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
- Push (the live unread badge): `internal/notifstream`. The consumer records
  through `notifstream.Recorder`, which wakes the per-recipient `Hub`; the
  stream handler in `internal/notificationapi` subscribes and writes SSE
  frames. Keep the hub a pure signal bus — the count is read from the DB, never
  carried in the signal.
- Auth (`internal/auth`), config, logging, readiness and the DB pool are adapted
  from credit-service; keep them in step when that service's copies change.

## Conventions

- Run `gofmt -w`, `go vet ./...` and `go test ./...` before committing.
- Integration tests skip unless their URL is set. Start a throwaway Postgres
  and RabbitMQ with `docker compose -f compose.test.yaml up -d`, then run them
  in one process (they share the DB and the broker queues):

  ```sh
  TEST_DATABASE_URL=postgres://notification_test@127.0.0.1:5435/notification_test?sslmode=disable \
  TEST_AMQP_URL=amqp://guest:guest@127.0.0.1:5673/ \
  go test -p 1 ./...
  ```
- The service applies no migrations (the runtime role has no DDL rights); add
  goose migrations under `migrations/` and let the migrate job apply them.
- Errors returned to callers use the stable codes in
  [internal/apperror/error.go](internal/apperror/error.go); never leak
  connection strings or provider error text.
