# Database migrations

Apply ordered Goose SQL migrations before starting admin-service, as the
**migrator** role (the schema owner), never as the runtime role. From
`admin-service/`, set `GOOSE_DRIVER=postgres` and `GOOSE_DBSTRING` through the
environment, then run:

```sh
go run github.com/pressly/goose/v3/cmd/goose@v3.28.0 -dir migrations up
```

Stop deployment if this command fails. The application does not apply DDL
itself, and its runtime role (`admin_service`) could not: it holds
`SELECT`, `INSERT` and `UPDATE` on the two fact tables and owns nothing.
Create both roles before the first migration (locally, `db-init/` does it; in
AWS see `docs/ecs-deployment-notes.md`). The migration grants the runtime role
its privileges when the role exists and skips the grant when it does not, so a
database without the role (a test database) still migrates. A new table needs
its own grant in its migration.

New migrations must be ordered and include `-- +goose Up` and
`-- +goose Down` sections. Do not edit migrations already applied in
production. The sqlc target in `../sqlc.yaml` reads this directory as its
schema, so run `go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate`
after adding a migration. The schema requires PostgreSQL 17 or later (the
`INCLUDE` index). Database tests apply every migration through the embedded
`migrations.Up()` script and skip unless `TEST_DATABASE_URL` is set.
