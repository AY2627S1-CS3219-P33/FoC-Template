BEGIN;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM accounts WHERE auth0_subject IS NOT NULL) THEN
        RAISE EXCEPTION 'cannot roll back Auth0 identity migration while Auth0 accounts exist';
    END IF;
    IF EXISTS (
        SELECT lower(username)
        FROM accounts
        GROUP BY lower(username)
        HAVING count(*) > 1
    ) THEN
        RAISE EXCEPTION 'cannot restore username uniqueness while duplicate usernames exist';
    END IF;
END
$$;

DROP INDEX accounts_auth0_subject_unique;

ALTER TABLE accounts
    DROP CONSTRAINT accounts_authentication_check,
    DROP COLUMN auth0_subject,
    ALTER COLUMN password_hash SET NOT NULL,
    ADD CONSTRAINT accounts_password_hash_check CHECK (password_hash <> '');

CREATE UNIQUE INDEX accounts_username_unique ON accounts (lower(username));

COMMIT;
