-- AI Assistance Disclosure:
-- Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-09
-- Scope: Implementation of SQL queries.
-- Author review: Reviewed, validated, and tested by the authors.

-- Requester enhancement queries (references/requester-enhancements-n2h.md),
-- generated into internal/database/requestergen. Saved places now; scheduled
-- errands reuse this file later.

-- name: LockUserPlaces :exec
-- LockUserPlaces serialises place inserts for one user, so the per-kind
-- limit counted by CountPlaces holds under concurrent creates. It must run
-- inside a transaction.
SELECT pg_advisory_xact_lock(hashtext('saved_places:' || sqlc.arg(user_id)::text));

-- name: ListPlaces :many
-- ListPlaces returns the user's places, newest first, of one kind or of both
-- when kind is NULL. It reads saved_places_user_idx.
SELECT id, user_id, kind, label, supplier_id, delivery_building_id,
       delivery_detail, created_at, updated_at
FROM saved_places
WHERE user_id = @user_id
  AND (sqlc.narg(kind)::text IS NULL OR kind = sqlc.narg(kind)::text)
ORDER BY created_at DESC, id DESC;

-- name: GetPlace :one
SELECT id, user_id, kind, label, supplier_id, delivery_building_id,
       delivery_detail, created_at, updated_at
FROM saved_places
WHERE id = @id AND user_id = @user_id;

-- name: CountPlaces :one
SELECT count(*) FROM saved_places
WHERE user_id = @user_id AND kind = @kind;

-- name: InsertPlace :one
INSERT INTO saved_places (
    user_id, kind, label, supplier_id, delivery_building_id, delivery_detail,
    created_at, updated_at
) VALUES (
    @user_id, @kind, @label, @supplier_id, @delivery_building_id, @delivery_detail,
    statement_timestamp(), statement_timestamp()
)
RETURNING id, user_id, kind, label, supplier_id, delivery_building_id,
          delivery_detail, created_at, updated_at;

-- name: UpdatePlace :one
-- UpdatePlace changes the label and the delivery detail; a NULL argument
-- keeps the stored value.
UPDATE saved_places
SET label = COALESCE(sqlc.narg(label)::text, label),
    delivery_detail = COALESCE(sqlc.narg(delivery_detail)::text, delivery_detail),
    updated_at = clock_timestamp()
WHERE id = @id AND user_id = @user_id
RETURNING id, user_id, kind, label, supplier_id, delivery_building_id,
          delivery_detail, created_at, updated_at;

-- name: DeletePlace :execrows
DELETE FROM saved_places
WHERE id = @id AND user_id = @user_id;
