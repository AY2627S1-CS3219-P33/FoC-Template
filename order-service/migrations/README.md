# Database migrations

Apply ordered Goose SQL migrations before starting the order service.
From `order-service/`, set `GOOSE_DRIVER=postgres` and `GOOSE_DBSTRING`
through the environment, then run:

```sh
go run github.com/pressly/goose/v3/cmd/goose@v3.28.0 -dir migrations up
```

Stop deployment if this command fails. The application does not apply DDL
itself.

New migrations must be ordered and include `-- +goose Up` and
`-- +goose Down` sections. Do not edit migrations already applied in production.
The schema is frozen in S0 (`references/commit-plan.md`): later schema changes
go through an integration commit, not a feature stream.

Both sqlc targets in `../sqlc.yaml` read this directory as their schema, so run
`go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate` after adding a
migration. The schema requires PostgreSQL 17 or later. Database tests apply
every migration through the embedded `migrations.Up()` script and skip unless
`TEST_DATABASE_URL` is set (see `../compose.test.yaml`).
