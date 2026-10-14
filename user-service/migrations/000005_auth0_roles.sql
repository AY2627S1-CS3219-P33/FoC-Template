-- +goose Up

ALTER TABLE bootstrap_state ADD COLUMN bootstrap_subject text;

UPDATE bootstrap_state
SET bootstrap_subject = (
    SELECT auth0_subject
    FROM accounts
    WHERE role = 'SUPER_ADMIN'
    ORDER BY created_at, id
    LIMIT 1
)
WHERE name = 'bootstrap_is_completed';

UPDATE bootstrap_state
SET completed_at = COALESCE(completed_at, now())
WHERE name = 'bootstrap_is_completed' AND bootstrap_subject IS NOT NULL;

ALTER TABLE accounts DROP COLUMN role;

-- +goose Down

ALTER TABLE accounts
    ADD COLUMN role text NOT NULL DEFAULT 'USER'
    CHECK (role IN ('USER', 'ADMIN', 'SUPER_ADMIN'));

UPDATE accounts
SET role = 'SUPER_ADMIN'
WHERE auth0_subject = (
    SELECT bootstrap_subject
    FROM bootstrap_state
    WHERE name = 'bootstrap_is_completed'
);

ALTER TABLE bootstrap_state DROP COLUMN bootstrap_subject;
