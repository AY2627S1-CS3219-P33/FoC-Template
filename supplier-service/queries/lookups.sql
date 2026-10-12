-- Administrator management of the buildings and supplier_types lookups. The
-- two tables have identical shapes, so each query has one variant per table.
-- Lists are ordered by lookup key then id so equal keys (a live row and the
-- rows it replaced) have a stable order.

-- name: ListBuildings :many
SELECT id::text AS id, name, (retired_at IS NOT NULL)::boolean AS retired
FROM buildings
WHERE sqlc.arg(include_retired)::boolean OR retired_at IS NULL
ORDER BY lookup_key, id
LIMIT sqlc.arg(row_limit);

-- name: ListSupplierTypes :many
SELECT id::text AS id, name, (retired_at IS NOT NULL)::boolean AS retired
FROM supplier_types
WHERE sqlc.arg(include_retired)::boolean OR retired_at IS NULL
ORDER BY lookup_key, id
LIMIT sqlc.arg(row_limit);

-- CreateBuilding and CreateSupplierType deliberately have no ON CONFLICT:
-- the live lookup-key index reports a duplicate as a unique violation, which
-- the repository maps to a conflict.

-- name: CreateBuilding :one
INSERT INTO buildings (name, created_at)
VALUES (sqlc.arg(name), sqlc.arg(now))
RETURNING id::text AS id, name;

-- name: CreateSupplierType :one
INSERT INTO supplier_types (name, created_at)
VALUES (sqlc.arg(name), sqlc.arg(now))
RETURNING id::text AS id, name;

-- name: FindLiveBuildingByKey :one
SELECT id::text AS id, name
FROM buildings
WHERE lookup_key = normalize_lookup_key(sqlc.arg(name)::text)
  AND retired_at IS NULL;

-- name: FindLiveSupplierTypeByKey :one
SELECT id::text AS id, name
FROM supplier_types
WHERE lookup_key = normalize_lookup_key(sqlc.arg(name)::text)
  AND retired_at IS NULL;

-- Retirement locks the row FOR UPDATE before checking use, so it serialises
-- with supplier writes that resolve the row FOR SHARE.

-- name: LockBuilding :one
SELECT id::text AS id, name, (retired_at IS NOT NULL)::boolean AS retired
FROM buildings
WHERE id = sqlc.arg(id)::text::uuid
FOR UPDATE;

-- name: LockSupplierType :one
SELECT id::text AS id, name, (retired_at IS NOT NULL)::boolean AS retired
FROM supplier_types
WHERE id = sqlc.arg(id)::text::uuid
FOR UPDATE;

-- Only the current version of a non-deleted supplier counts as use:
-- superseded and deleted-supplier versions keep resolving a retired row.

-- name: CountSuppliersUsingBuilding :one
SELECT count(*)::integer AS suppliers
FROM suppliers AS s
JOIN supplier_versions AS v ON v.version_id = s.current_version_id
WHERE s.deleted_at IS NULL
  AND v.building_id = sqlc.arg(id)::text::uuid;

-- name: CountSuppliersUsingSupplierType :one
SELECT count(*)::integer AS suppliers
FROM suppliers AS s
JOIN supplier_versions AS v ON v.version_id = s.current_version_id
WHERE s.deleted_at IS NULL
  AND v.supplier_type_id = sqlc.arg(id)::text::uuid;

-- GREATEST keeps the retired_after_creation check satisfied when the
-- application clock is slightly behind the database clock.

-- name: RetireBuilding :exec
UPDATE buildings
SET retired_at = GREATEST(sqlc.arg(now)::timestamptz, created_at)
WHERE id = sqlc.arg(id)::text::uuid
  AND retired_at IS NULL;

-- name: RetireSupplierType :exec
UPDATE supplier_types
SET retired_at = GREATEST(sqlc.arg(now)::timestamptz, created_at)
WHERE id = sqlc.arg(id)::text::uuid
  AND retired_at IS NULL;
