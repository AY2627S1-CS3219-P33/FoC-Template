-- +goose Up

-- A user finds their own wallet by the Auth0 subject in their access token.
-- user-service supplies it when it creates the wallet. It is immutable by
-- convention and unique, so one identity can never own two wallets.
-- No wallets exist when this runs; adding the column NOT NULL fails loudly
-- if that ever stops being true.
ALTER TABLE wallets
    ADD COLUMN auth0_subject text NOT NULL,
    ADD CONSTRAINT wallets_auth0_subject_key UNIQUE (auth0_subject),
    ADD CONSTRAINT wallets_auth0_subject_valid
        CHECK (auth0_subject = btrim(auth0_subject) AND auth0_subject <> '');

-- +goose Down

ALTER TABLE wallets
    DROP CONSTRAINT wallets_auth0_subject_valid,
    DROP CONSTRAINT wallets_auth0_subject_key,
    DROP COLUMN auth0_subject;
