-- AI Assistance Disclosure:
-- Tool: Claude, date: 2026-11-11
-- Scope: Database schema migrations for notification-service.
-- Author review: I validated correctness, edited for style, and added boundary checks.

-- +goose Up

-- Runs as migrator. Gives the notification runtime role data access only: it can
-- read and write rows but cannot create, alter or drop tables.
-- Guarded so the migration also applies where the runtime role does not exist (the
-- throwaway test database and local compose, which run as the owner role).
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT FROM pg_roles WHERE rolname = 'notification_service_user') THEN
        GRANT USAGE ON SCHEMA public TO notification_service_user;
        -- Tables migrator creates from now on are usable by the service.
        ALTER DEFAULT PRIVILEGES IN SCHEMA public
            GRANT SELECT, INSERT, UPDATE ON TABLES TO notification_service_user;
        ALTER DEFAULT PRIVILEGES IN SCHEMA public
            GRANT USAGE, SELECT ON SEQUENCES TO notification_service_user;
        -- goose creates this table before any default privileges exist; the
        -- service reads it to report schema version in /readyz.
        GRANT SELECT ON goose_db_version TO notification_service_user;
    END IF;
END
$$;
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT FROM pg_roles WHERE rolname = 'notification_service_user') THEN
        REVOKE SELECT ON goose_db_version FROM notification_service_user;
        ALTER DEFAULT PRIVILEGES IN SCHEMA public
            REVOKE USAGE, SELECT ON SEQUENCES FROM notification_service_user;
        ALTER DEFAULT PRIVILEGES IN SCHEMA public
            REVOKE SELECT, INSERT, UPDATE ON TABLES FROM notification_service_user;
        REVOKE USAGE ON SCHEMA public FROM notification_service_user;
    END IF;
END
$$;
-- +goose StatementEnd
