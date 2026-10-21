-- +goose Up

-- Runs as migrator. Gives the runtime role app_rw data access only: it can
-- read and write rows but cannot create, alter or drop tables.
-- Guarded so the migration also applies where app_rw does not exist (the
-- throwaway test database).
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT FROM pg_roles WHERE rolname = 'app_rw') THEN
        GRANT USAGE ON SCHEMA public TO app_rw;
        -- Tables migrator creates from now on are usable by app_rw.
        ALTER DEFAULT PRIVILEGES IN SCHEMA public
            GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO app_rw;
        ALTER DEFAULT PRIVILEGES IN SCHEMA public
            GRANT USAGE, SELECT ON SEQUENCES TO app_rw;
        -- goose creates this table before any default privileges exist; the
        -- service reads it to report schema version in /readyz.
        GRANT SELECT ON goose_db_version TO app_rw;
    END IF;
END
$$;
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT FROM pg_roles WHERE rolname = 'app_rw') THEN
        REVOKE SELECT ON goose_db_version FROM app_rw;
        ALTER DEFAULT PRIVILEGES IN SCHEMA public
            REVOKE USAGE, SELECT ON SEQUENCES FROM app_rw;
        ALTER DEFAULT PRIVILEGES IN SCHEMA public
            REVOKE SELECT, INSERT, UPDATE, DELETE ON TABLES FROM app_rw;
        REVOKE USAGE ON SCHEMA public FROM app_rw;
    END IF;
END
$$;
-- +goose StatementEnd
