-- One-time bootstrap, run as the RDS master user (see bootstrap.sh).
-- Creates the migrator and app_rw roles and the application database.
-- Safe to re-run: roles and database are only created when missing, and
-- passwords and ownership are re-applied. Contains no secrets; passwords come
-- from the MIGRATOR_PW, APP_PW and APP_DB environment variables.
\set ON_ERROR_STOP on

\set migrator_pw `printenv MIGRATOR_PW`
\set app_pw `printenv APP_PW`
\set app_db `printenv APP_DB`

-- Roles are cluster-wide. migrator owns the schema; app_rw is the runtime role.
SELECT 'CREATE ROLE migrator LOGIN'
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'migrator') \gexec
SELECT 'CREATE ROLE app_rw LOGIN'
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'app_rw') \gexec

SELECT format('ALTER ROLE migrator PASSWORD %L', :'migrator_pw') \gexec
SELECT format('ALTER ROLE app_rw PASSWORD %L', :'app_pw') \gexec

-- RDS master is not a superuser: it must be a member of migrator to make it
-- the owner of a database.
GRANT migrator TO CURRENT_USER;

SELECT format('CREATE DATABASE %I OWNER migrator', :'app_db')
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = :'app_db') \gexec

-- Covers an initial database that RDS created, which the master owns.
SELECT format('ALTER DATABASE %I OWNER TO migrator', :'app_db') \gexec

-- Only the two roles may connect to the application database.
SELECT format('REVOKE CONNECT ON DATABASE %I FROM PUBLIC', :'app_db') \gexec
SELECT format('GRANT CONNECT ON DATABASE %I TO migrator, app_rw', :'app_db') \gexec
