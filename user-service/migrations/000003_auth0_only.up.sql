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
    ADD CONSTRAINT accounts_auth0_subject_check CHECK (
        auth0_subject = btrim(auth0_subject) AND auth0_subject <> ''
    ),
    DROP COLUMN password_hash;

COMMIT;
