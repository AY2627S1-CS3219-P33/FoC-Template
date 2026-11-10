#!/bin/sh
# Local development only. Creates the runtime role next to the migrator role
# (the container's POSTGRES_USER, which owns the schema). The runtime role
# gets data access only; migrations grant it table privileges. In AWS the
# same two roles are created on the shared RDS instance (docs/ecs-deployment-notes.md).
set -eu
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<SQL
CREATE ROLE admin_service LOGIN PASSWORD '${ADMIN_RUNTIME_PASSWORD}' NOSUPERUSER NOCREATEDB NOCREATEROLE;
GRANT CONNECT ON DATABASE ${POSTGRES_DB} TO admin_service;
GRANT USAGE ON SCHEMA public TO admin_service;
SQL
