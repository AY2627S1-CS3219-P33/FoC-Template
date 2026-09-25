BEGIN;

ALTER TABLE accounts
    DROP CONSTRAINT accounts_password_hash_check,
    ALTER COLUMN password_hash DROP NOT NULL,
    ADD COLUMN auth0_subject text,
    ADD CONSTRAINT accounts_authentication_check CHECK (
        (password_hash IS NOT NULL AND password_hash <> '' AND auth0_subject IS NULL)
        OR
        (password_hash IS NULL AND auth0_subject IS NOT NULL AND auth0_subject = btrim(auth0_subject) AND auth0_subject <> '')
    );

-- Auth0 subjects remain reserved after soft deletion, just like email addresses.
CREATE UNIQUE INDEX accounts_auth0_subject_unique
    ON accounts (auth0_subject)
    WHERE auth0_subject IS NOT NULL;

COMMIT;
