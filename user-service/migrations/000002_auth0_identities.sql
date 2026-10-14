-- +goose Up

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

-- +goose Down

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM accounts WHERE auth0_subject IS NOT NULL) THEN
        RAISE EXCEPTION 'cannot roll back Auth0 identity migration while Auth0 accounts exist';
    END IF;
END
$$;
-- +goose StatementEnd

DROP INDEX accounts_auth0_subject_unique;

ALTER TABLE accounts
    DROP CONSTRAINT accounts_authentication_check,
    DROP COLUMN auth0_subject,
    ALTER COLUMN password_hash SET NOT NULL,
    ADD CONSTRAINT accounts_password_hash_check CHECK (password_hash <> '');
