-- Outbox claiming, delivery and credit-result queries, owned by Person C and
-- generated into internal/database/msggen.
--
-- PlaceholderMessaging exists only so `sqlc generate` succeeds before M1.
-- C replaces it with the real queries.

-- name: PlaceholderMessaging :one
SELECT 1::integer AS placeholder;
