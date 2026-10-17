#!/usr/bin/env bash
# One-time RDS bootstrap: roles and database. Run as the master user.
set -euo pipefail
cd "$(dirname "$0")/.."

[ -f .env.admin ] || { echo ".env.admin not found (copy .env.admin.example)" >&2; exit 1; }
set -a; source .env.admin; set +a

for name in PGHOST PGUSER PGPASSWORD APP_DB MIGRATOR_PW APP_PW; do
  [ -n "${!name:-}" ] || { echo "$name is empty in .env.admin" >&2; exit 1; }
done

# Connect to the maintenance database; the application database may not exist yet.
PGDATABASE=postgres psql -v ON_ERROR_STOP=1 -f infra/bootstrap.sql
