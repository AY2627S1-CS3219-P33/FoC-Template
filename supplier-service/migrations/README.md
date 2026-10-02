# Database migrations

Apply ordered Goose SQL migrations before starting the supplier service.
From `supplier-service/`, set `GOOSE_DRIVER=postgres` and `GOOSE_DBSTRING`
through the environment, then run:

```sh
go run github.com/pressly/goose/v3/cmd/goose@v3.28.0 -dir migrations up
```

Stop deployment if this command fails. The application then imports its
configured seed CSV before serving HTTP; it does not apply DDL itself.
See [the startup sequence](../README.md#local-setup).

New migrations must be ordered and include `-- +goose Up` and
`-- +goose Down` sections. Do not edit migrations already applied in production.
