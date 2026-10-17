#!/usr/bin/env bash
# Applies migrations as the migrator role. Usage: infra/migrate.sh [goose command]
# Default command is "up"; try "status" to see applied versions.
set -euo pipefail
cd "$(dirname "$0")/.."

[ -f .env.admin ] || { echo ".env.admin not found (copy .env.admin.example)" >&2; exit 1; }
set -a; source .env.admin; set +a

for name in PGHOST APP_DB MIGRATOR_PW; do
  [ -n "${!name:-}" ] || { echo "$name is empty in .env.admin" >&2; exit 1; }
done

export GOOSE_DRIVER=postgres
export GOOSE_MIGRATION_DIR=./migrations
# Passwords from `openssl rand -hex` are URL-safe; escape them if you choose your own.
export GOOSE_DBSTRING="postgres://migrator:${MIGRATOR_PW}@${PGHOST}:${PGPORT:-5432}/${APP_DB}?sslmode=${PGSSLMODE:-require}"

if command -v goose >/dev/null 2>&1; then
  goose "${@:-up}"
else
  go run github.com/pressly/goose/v3/cmd/goose@v3.29.0 "${@:-up}"
fi
