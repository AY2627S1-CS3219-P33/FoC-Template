BEGIN;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM accounts WHERE auth0_subject IS NULL) THEN
        RAISE EXCEPTION 'cannot remove local passwords while password-authenticated accounts exist';
    END IF;
END $$;

ALTER TABLE accounts
    DROP CONSTRAINT accounts_authentication_check,
    ALTER COLUMN auth0_subject SET NOT NULL,
    DROP COLUMN password_hash;

COMMIT;
