BEGIN;

CREATE TABLE accounts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    username text NOT NULL CHECK (char_length(username) BETWEEN 1 AND 64 AND username = btrim(username)),
    email text NOT NULL CHECK (char_length(email) BETWEEN 1 AND 254 AND email = lower(btrim(email))),
    password_hash text NOT NULL CHECK (password_hash <> ''),
    role text NOT NULL DEFAULT 'USER' CHECK (role IN ('USER', 'ADMIN', 'SUPER_ADMIN')),
    active boolean NOT NULL DEFAULT false,
    display_name text NOT NULL DEFAULT '' CHECK (char_length(display_name) <= 100),
    mobile_number text NOT NULL DEFAULT '' CHECK (char_length(mobile_number) <= 32),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz,
    CHECK (deleted_at IS NULL OR NOT active)
);

-- Non-partial indexes intentionally reserve identities after soft deletion.
CREATE UNIQUE INDEX accounts_username_unique ON accounts (lower(username));
CREATE UNIQUE INDEX accounts_email_unique ON accounts (lower(email));

COMMIT;
