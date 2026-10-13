-- Outbox, fence, review, audit and settings queries, owned by Person B and
-- generated into internal/database/opsgen.
--
-- PlaceholderOps exists only so `sqlc generate` succeeds before B1.
-- B replaces it with the real queries.

-- name: PlaceholderOps :one
SELECT 1::integer AS placeholder;
