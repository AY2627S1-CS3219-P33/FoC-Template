-- +goose Up

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM accounts WHERE auth0_subject IS NULL) THEN
        RAISE EXCEPTION 'cannot remove local passwords while password-authenticated accounts exist';
    END IF;
END $$;
-- +goose StatementEnd

ALTER TABLE accounts
    DROP CONSTRAINT accounts_authentication_check,
    ALTER COLUMN auth0_subject SET NOT NULL,
    ADD CONSTRAINT accounts_auth0_subject_check CHECK (
        auth0_subject = btrim(auth0_subject) AND auth0_subject <> ''
    ),
    DROP COLUMN password_hash;

-- +goose Down

ALTER TABLE accounts
    DROP CONSTRAINT accounts_auth0_subject_check,
    ADD COLUMN password_hash text,
    ALTER COLUMN auth0_subject DROP NOT NULL,
    ADD CONSTRAINT accounts_authentication_check CHECK (
        (password_hash IS NOT NULL AND password_hash <> '' AND auth0_subject IS NULL)
        OR
        (password_hash IS NULL AND auth0_subject IS NOT NULL AND auth0_subject = btrim(auth0_subject) AND auth0_subject <> '')
    );
