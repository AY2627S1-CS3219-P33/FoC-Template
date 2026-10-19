# Applying service migrations to RDS

This note records the working setup for the shared PostgreSQL RDS instance.
Run commands from the repository root, from a machine that can reach RDS.

## Connection setup

Download the RDS CA bundle once, then set the endpoint in your shell:

```sh
curl -o global-bundle.pem https://truststore.pki.rds.amazonaws.com/global/global-bundle.pem
export RDSHOST="foc-shared-database.cn22asqiuahb.ap-southeast-1.rds.amazonaws.com"
```

Store credentials in `~/.pgpass`, one line per database, in this format:

```text
hostname:port:database:username:password
```

For example, entries are needed for `user_service_db` / `user_service_user`
and `supplier_service_db` / `supplier_service_user`. Edit the file without
printing its contents, then restrict access:

```sh
chmod 0600 ~/.pgpass
```

If a password contains `:` or `\`, escape that character with `\` in the
`.pgpass` entry. Do not commit `~/.pgpass`, `global-bundle.pem`, or credentials.

## Apply migrations

The Goose CLI is not installed globally. Run the project-pinned version with
`go run`; the service migration READMEs pin Goose at `v3.28.0`.

```sh
go run github.com/pressly/goose/v3/cmd/goose@v3.28.0 \
  -dir ./user-service/migrations postgres \
  "host=$RDSHOST port=5432 user=user_service_user dbname=user_service_db sslmode=verify-full sslrootcert=./global-bundle.pem" up
```

```sh
go run github.com/pressly/goose/v3/cmd/goose@v3.28.0 \
  -dir ./supplier-service/migrations postgres \
  "host=$RDSHOST port=5432 user=supplier_service_user dbname=supplier_service_db sslmode=verify-full sslrootcert=./global-bundle.pem" up
```

Goose uses the matching `.pgpass` entry. Check the migration status with the
same command and replace `up` with `status` before or after applying. The
observed RDS migrations completed at user-service version 5 and supplier-service
version 2. Applying migrations again is safe; Goose skips versions already
recorded as applied.

Use each service's own database and migration credentials. Do not use a shared
superuser for application services. The RDS database owner listing does not
confirm a role's login status or password; `\du role_name` in `psql` shows
whether it can log in.
