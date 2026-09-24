BEGIN;

ALTER TABLE accounts
    ADD COLUMN password_hash text,
    ALTER COLUMN auth0_subject DROP NOT NULL,
    ADD CONSTRAINT accounts_authentication_check CHECK (
        (password_hash IS NOT NULL AND password_hash <> '' AND auth0_subject IS NULL)
        OR
        (password_hash IS NULL AND auth0_subject IS NOT NULL AND auth0_subject = btrim(auth0_subject) AND auth0_subject <> '')
    );

COMMIT;
