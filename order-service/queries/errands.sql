-- Errand lifecycle queries, owned by Person A and generated into
-- internal/database/errandgen.
--
-- PlaceholderErrands exists only so `sqlc generate` succeeds before A1.
-- A replaces it with the real queries.

-- name: PlaceholderErrands :one
SELECT 1::integer AS placeholder;
