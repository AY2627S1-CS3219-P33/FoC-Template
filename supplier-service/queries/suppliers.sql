-- name: InsertSupplierIdentity :one
INSERT INTO suppliers (
    current_version_id,
    current_normalized_name,
    created_at,
    updated_at
) VALUES (
    gen_random_uuid(),
    sqlc.arg(normalized_name),
    sqlc.arg(now),
    sqlc.arg(now)
)
RETURNING
    supplier_id::text AS supplier_id,
    current_version_id::text AS version_id,
    created_at,
    updated_at;

-- name: InsertSupplierVersion :exec
INSERT INTO supplier_versions (
    version_id,
    supplier_id,
    name,
    supplier_type_id,
    building_id,
    floor,
    location_description,
    latitude,
    longitude,
    opening_time,
    closing_time,
    image_url,
    created_at
) VALUES (
    sqlc.arg(version_id)::text::uuid,
    sqlc.arg(supplier_id)::text::uuid,
    sqlc.arg(name),
    sqlc.arg(supplier_type_id)::text::uuid,
    sqlc.arg(building_id)::text::uuid,
    sqlc.arg(floor),
    sqlc.arg(location_description),
    sqlc.arg(latitude),
    sqlc.arg(longitude),
    sqlc.arg(opening_time)::text::time,
    sqlc.arg(closing_time)::text::time,
    NULLIF(sqlc.arg(image_url)::text, ''),
    sqlc.arg(now)
);

-- name: GenerateSupplierVersionID :one
SELECT gen_random_uuid()::text AS version_id;

-- name: LockCurrentSupplier :one
SELECT
    s.current_version_id::text AS version_id,
    s.created_at,
    s.updated_at
FROM suppliers AS s
WHERE s.supplier_id = sqlc.arg(supplier_id)::text::uuid
  AND s.deleted_at IS NULL
FOR UPDATE;

-- name: ReplaceCurrentSupplierVersion :one
UPDATE suppliers
SET current_version_id = sqlc.arg(version_id)::text::uuid,
    current_normalized_name = sqlc.arg(normalized_name),
    updated_at = sqlc.arg(now)
WHERE supplier_id = sqlc.arg(supplier_id)::text::uuid
  AND deleted_at IS NULL
RETURNING created_at, updated_at;

-- name: GetCurrentAvailableSupplier :one
SELECT
    s.supplier_id::text AS supplier_id,
    s.current_version_id::text AS version_id,
    v.name,
    t.name AS supplier_type,
    t.id::text AS supplier_type_id,
    b.name AS building,
    b.id::text AS building_id,
    v.floor,
    v.location_description,
    v.latitude,
    v.longitude,
    to_char(v.opening_time, 'HH24:MI') AS opening_time,
    to_char(v.closing_time, 'HH24:MI') AS closing_time,
    COALESCE(v.image_url, '') AS image_url,
    s.created_at,
    s.updated_at
FROM suppliers AS s
JOIN supplier_versions AS v
  ON v.supplier_id = s.supplier_id
 AND v.version_id = s.current_version_id
JOIN buildings AS b ON b.id = v.building_id
JOIN supplier_types AS t ON t.id = v.supplier_type_id
WHERE s.supplier_id = sqlc.arg(supplier_id)::text::uuid
  AND s.deleted_at IS NULL;

-- name: GetSupplierVersion :one
SELECT
    v.supplier_id::text AS supplier_id,
    v.version_id::text AS version_id,
    v.name,
    t.name AS supplier_type,
    t.id::text AS supplier_type_id,
    b.name AS building,
    b.id::text AS building_id,
    v.floor,
    v.location_description,
    v.latitude,
    v.longitude,
    to_char(v.opening_time, 'HH24:MI') AS opening_time,
    to_char(v.closing_time, 'HH24:MI') AS closing_time,
    COALESCE(v.image_url, '') AS image_url,
    v.created_at,
    (s.deleted_at IS NULL AND s.current_version_id = v.version_id) AS available
FROM supplier_versions AS v
JOIN suppliers AS s ON s.supplier_id = v.supplier_id
JOIN buildings AS b ON b.id = v.building_id
JOIN supplier_types AS t ON t.id = v.supplier_type_id
WHERE v.version_id = sqlc.arg(version_id)::text::uuid;

-- The ListAvailableSuppliers* queries share one projection and filter and
-- differ only in sort key. Each keyset predicate compares the same tuple, in
-- the same direction, as its ORDER BY so pages never overlap or skip rows.
-- Descending sorts reverse the whole key, so they are exactly the ascending
-- sequence read backwards.

-- name: ListAvailableSuppliersByName :many
SELECT
    s.supplier_id::text AS supplier_id,
    s.current_version_id::text AS version_id,
    v.name,
    t.name AS supplier_type,
    t.id::text AS supplier_type_id,
    b.name AS building,
    b.id::text AS building_id,
    v.floor,
    v.location_description,
    v.latitude,
    v.longitude,
    to_char(v.opening_time, 'HH24:MI') AS opening_time,
    to_char(v.closing_time, 'HH24:MI') AS closing_time,
    COALESCE(v.image_url, '') AS image_url,
    s.created_at,
    s.updated_at,
    s.current_normalized_name AS normalized_name
FROM suppliers AS s
JOIN supplier_versions AS v
  ON v.supplier_id = s.supplier_id
 AND v.version_id = s.current_version_id
JOIN buildings AS b ON b.id = v.building_id
JOIN supplier_types AS t ON t.id = v.supplier_type_id
WHERE s.deleted_at IS NULL
  AND (
      sqlc.arg(query_text)::text = ''
      OR strpos(s.current_normalized_name, normalize_supplier_name(sqlc.arg(query_text)::text)) > 0
      OR strpos(normalize_supplier_name(b.name), normalize_supplier_name(sqlc.arg(query_text)::text)) > 0
      OR strpos(normalize_supplier_name(v.floor), normalize_supplier_name(sqlc.arg(query_text)::text)) > 0
      OR strpos(normalize_supplier_name(v.location_description), normalize_supplier_name(sqlc.arg(query_text)::text)) > 0
  )
  AND (
      sqlc.arg(supplier_type)::text = ''
      OR t.lookup_key = normalize_lookup_key(sqlc.arg(supplier_type)::text)
  )
  AND (
      NOT sqlc.arg(has_cursor)::boolean
      OR (s.current_normalized_name, s.supplier_id) >
         (sqlc.arg(cursor_name)::text, NULLIF(sqlc.arg(cursor_supplier_id)::text, '')::uuid)
  )
ORDER BY s.current_normalized_name, s.supplier_id
LIMIT sqlc.arg(page_limit);

-- name: ListAvailableSuppliersByNameDesc :many
SELECT
    s.supplier_id::text AS supplier_id,
    s.current_version_id::text AS version_id,
    v.name,
    t.name AS supplier_type,
    t.id::text AS supplier_type_id,
    b.name AS building,
    b.id::text AS building_id,
    v.floor,
    v.location_description,
    v.latitude,
    v.longitude,
    to_char(v.opening_time, 'HH24:MI') AS opening_time,
    to_char(v.closing_time, 'HH24:MI') AS closing_time,
    COALESCE(v.image_url, '') AS image_url,
    s.created_at,
    s.updated_at,
    s.current_normalized_name AS normalized_name
FROM suppliers AS s
JOIN supplier_versions AS v
  ON v.supplier_id = s.supplier_id
 AND v.version_id = s.current_version_id
JOIN buildings AS b ON b.id = v.building_id
JOIN supplier_types AS t ON t.id = v.supplier_type_id
WHERE s.deleted_at IS NULL
  AND (
      sqlc.arg(query_text)::text = ''
      OR strpos(s.current_normalized_name, normalize_supplier_name(sqlc.arg(query_text)::text)) > 0
      OR strpos(normalize_supplier_name(b.name), normalize_supplier_name(sqlc.arg(query_text)::text)) > 0
      OR strpos(normalize_supplier_name(v.floor), normalize_supplier_name(sqlc.arg(query_text)::text)) > 0
      OR strpos(normalize_supplier_name(v.location_description), normalize_supplier_name(sqlc.arg(query_text)::text)) > 0
  )
  AND (
      sqlc.arg(supplier_type)::text = ''
      OR t.lookup_key = normalize_lookup_key(sqlc.arg(supplier_type)::text)
  )
  AND (
      NOT sqlc.arg(has_cursor)::boolean
      OR (s.current_normalized_name, s.supplier_id) <
         (sqlc.arg(cursor_name)::text, NULLIF(sqlc.arg(cursor_supplier_id)::text, '')::uuid)
  )
ORDER BY s.current_normalized_name DESC, s.supplier_id DESC
LIMIT sqlc.arg(page_limit);

-- name: ListAvailableSuppliersByType :many
-- cursor_type is the anchor version's type name; normalize_lookup_key turns
-- it into the sort key even if that type has since been retired.
SELECT
    s.supplier_id::text AS supplier_id,
    s.current_version_id::text AS version_id,
    v.name,
    t.name AS supplier_type,
    t.id::text AS supplier_type_id,
    b.name AS building,
    b.id::text AS building_id,
    v.floor,
    v.location_description,
    v.latitude,
    v.longitude,
    to_char(v.opening_time, 'HH24:MI') AS opening_time,
    to_char(v.closing_time, 'HH24:MI') AS closing_time,
    COALESCE(v.image_url, '') AS image_url,
    s.created_at,
    s.updated_at,
    s.current_normalized_name AS normalized_name
FROM suppliers AS s
JOIN supplier_versions AS v
  ON v.supplier_id = s.supplier_id
 AND v.version_id = s.current_version_id
JOIN buildings AS b ON b.id = v.building_id
JOIN supplier_types AS t ON t.id = v.supplier_type_id
WHERE s.deleted_at IS NULL
  AND (
      sqlc.arg(query_text)::text = ''
      OR strpos(s.current_normalized_name, normalize_supplier_name(sqlc.arg(query_text)::text)) > 0
      OR strpos(normalize_supplier_name(b.name), normalize_supplier_name(sqlc.arg(query_text)::text)) > 0
      OR strpos(normalize_supplier_name(v.floor), normalize_supplier_name(sqlc.arg(query_text)::text)) > 0
      OR strpos(normalize_supplier_name(v.location_description), normalize_supplier_name(sqlc.arg(query_text)::text)) > 0
  )
  AND (
      sqlc.arg(supplier_type)::text = ''
      OR t.lookup_key = normalize_lookup_key(sqlc.arg(supplier_type)::text)
  )
  AND (
      NOT sqlc.arg(has_cursor)::boolean
      OR (t.lookup_key, s.current_normalized_name, s.supplier_id) >
         (normalize_lookup_key(sqlc.arg(cursor_type)::text), sqlc.arg(cursor_name)::text, NULLIF(sqlc.arg(cursor_supplier_id)::text, '')::uuid)
  )
ORDER BY t.lookup_key, s.current_normalized_name, s.supplier_id
LIMIT sqlc.arg(page_limit);

-- name: ListAvailableSuppliersByTypeDesc :many
SELECT
    s.supplier_id::text AS supplier_id,
    s.current_version_id::text AS version_id,
    v.name,
    t.name AS supplier_type,
    t.id::text AS supplier_type_id,
    b.name AS building,
    b.id::text AS building_id,
    v.floor,
    v.location_description,
    v.latitude,
    v.longitude,
    to_char(v.opening_time, 'HH24:MI') AS opening_time,
    to_char(v.closing_time, 'HH24:MI') AS closing_time,
    COALESCE(v.image_url, '') AS image_url,
    s.created_at,
    s.updated_at,
    s.current_normalized_name AS normalized_name
FROM suppliers AS s
JOIN supplier_versions AS v
  ON v.supplier_id = s.supplier_id
 AND v.version_id = s.current_version_id
JOIN buildings AS b ON b.id = v.building_id
JOIN supplier_types AS t ON t.id = v.supplier_type_id
WHERE s.deleted_at IS NULL
  AND (
      sqlc.arg(query_text)::text = ''
      OR strpos(s.current_normalized_name, normalize_supplier_name(sqlc.arg(query_text)::text)) > 0
      OR strpos(normalize_supplier_name(b.name), normalize_supplier_name(sqlc.arg(query_text)::text)) > 0
      OR strpos(normalize_supplier_name(v.floor), normalize_supplier_name(sqlc.arg(query_text)::text)) > 0
      OR strpos(normalize_supplier_name(v.location_description), normalize_supplier_name(sqlc.arg(query_text)::text)) > 0
  )
  AND (
      sqlc.arg(supplier_type)::text = ''
      OR t.lookup_key = normalize_lookup_key(sqlc.arg(supplier_type)::text)
  )
  AND (
      NOT sqlc.arg(has_cursor)::boolean
      OR (t.lookup_key, s.current_normalized_name, s.supplier_id) <
         (normalize_lookup_key(sqlc.arg(cursor_type)::text), sqlc.arg(cursor_name)::text, NULLIF(sqlc.arg(cursor_supplier_id)::text, '')::uuid)
  )
ORDER BY t.lookup_key DESC, s.current_normalized_name DESC, s.supplier_id DESC
LIMIT sqlc.arg(page_limit);

-- name: ListAvailableSuppliersByBuilding :many
-- cursor_building is the anchor version's building name; normalize_lookup_key
-- turns it into the sort key even if that building has since been retired.
SELECT
    s.supplier_id::text AS supplier_id,
    s.current_version_id::text AS version_id,
    v.name,
    t.name AS supplier_type,
    t.id::text AS supplier_type_id,
    b.name AS building,
    b.id::text AS building_id,
    v.floor,
    v.location_description,
    v.latitude,
    v.longitude,
    to_char(v.opening_time, 'HH24:MI') AS opening_time,
    to_char(v.closing_time, 'HH24:MI') AS closing_time,
    COALESCE(v.image_url, '') AS image_url,
    s.created_at,
    s.updated_at,
    s.current_normalized_name AS normalized_name
FROM suppliers AS s
JOIN supplier_versions AS v
  ON v.supplier_id = s.supplier_id
 AND v.version_id = s.current_version_id
JOIN buildings AS b ON b.id = v.building_id
JOIN supplier_types AS t ON t.id = v.supplier_type_id
WHERE s.deleted_at IS NULL
  AND (
      sqlc.arg(query_text)::text = ''
      OR strpos(s.current_normalized_name, normalize_supplier_name(sqlc.arg(query_text)::text)) > 0
      OR strpos(normalize_supplier_name(b.name), normalize_supplier_name(sqlc.arg(query_text)::text)) > 0
      OR strpos(normalize_supplier_name(v.floor), normalize_supplier_name(sqlc.arg(query_text)::text)) > 0
      OR strpos(normalize_supplier_name(v.location_description), normalize_supplier_name(sqlc.arg(query_text)::text)) > 0
  )
  AND (
      sqlc.arg(supplier_type)::text = ''
      OR t.lookup_key = normalize_lookup_key(sqlc.arg(supplier_type)::text)
  )
  AND (
      NOT sqlc.arg(has_cursor)::boolean
      OR (b.lookup_key, s.current_normalized_name, s.supplier_id) >
         (normalize_lookup_key(sqlc.arg(cursor_building)::text), sqlc.arg(cursor_name)::text, NULLIF(sqlc.arg(cursor_supplier_id)::text, '')::uuid)
  )
ORDER BY b.lookup_key, s.current_normalized_name, s.supplier_id
LIMIT sqlc.arg(page_limit);

-- name: ListAvailableSuppliersByBuildingDesc :many
SELECT
    s.supplier_id::text AS supplier_id,
    s.current_version_id::text AS version_id,
    v.name,
    t.name AS supplier_type,
    t.id::text AS supplier_type_id,
    b.name AS building,
    b.id::text AS building_id,
    v.floor,
    v.location_description,
    v.latitude,
    v.longitude,
    to_char(v.opening_time, 'HH24:MI') AS opening_time,
    to_char(v.closing_time, 'HH24:MI') AS closing_time,
    COALESCE(v.image_url, '') AS image_url,
    s.created_at,
    s.updated_at,
    s.current_normalized_name AS normalized_name
FROM suppliers AS s
JOIN supplier_versions AS v
  ON v.supplier_id = s.supplier_id
 AND v.version_id = s.current_version_id
JOIN buildings AS b ON b.id = v.building_id
JOIN supplier_types AS t ON t.id = v.supplier_type_id
WHERE s.deleted_at IS NULL
  AND (
      sqlc.arg(query_text)::text = ''
      OR strpos(s.current_normalized_name, normalize_supplier_name(sqlc.arg(query_text)::text)) > 0
      OR strpos(normalize_supplier_name(b.name), normalize_supplier_name(sqlc.arg(query_text)::text)) > 0
      OR strpos(normalize_supplier_name(v.floor), normalize_supplier_name(sqlc.arg(query_text)::text)) > 0
      OR strpos(normalize_supplier_name(v.location_description), normalize_supplier_name(sqlc.arg(query_text)::text)) > 0
  )
  AND (
      sqlc.arg(supplier_type)::text = ''
      OR t.lookup_key = normalize_lookup_key(sqlc.arg(supplier_type)::text)
  )
  AND (
      NOT sqlc.arg(has_cursor)::boolean
      OR (b.lookup_key, s.current_normalized_name, s.supplier_id) <
         (normalize_lookup_key(sqlc.arg(cursor_building)::text), sqlc.arg(cursor_name)::text, NULLIF(sqlc.arg(cursor_supplier_id)::text, '')::uuid)
  )
ORDER BY b.lookup_key DESC, s.current_normalized_name DESC, s.supplier_id DESC
LIMIT sqlc.arg(page_limit);

-- name: NormalizedSupplierNameExists :one
SELECT EXISTS (
    SELECT 1
    FROM suppliers
    WHERE deleted_at IS NULL
      AND current_normalized_name = sqlc.arg(normalized_name)
      AND (
          sqlc.arg(exclude_supplier_id)::text = ''
          OR supplier_id <> NULLIF(sqlc.arg(exclude_supplier_id)::text, '')::uuid
      )
) AS name_exists;

-- Building and supplier-type lookups. Resolution considers live rows only;
-- reads of existing versions join by ID, so retired rows still resolve there.
-- FOR SHARE pairs with the FOR UPDATE taken by RetireBuilding and
-- RetireSupplierType: a write that resolves a row blocks its retirement until
-- the write commits, and a write that waits on a retirement re-checks
-- retired_at and no longer sees the row.

-- name: ResolveBuildingByKey :one
SELECT id::text AS id, name
FROM buildings
WHERE lookup_key = normalize_lookup_key(sqlc.arg(name)::text)
  AND retired_at IS NULL
FOR SHARE;

-- name: ResolveSupplierTypeByKey :one
SELECT id::text AS id, name
FROM supplier_types
WHERE lookup_key = normalize_lookup_key(sqlc.arg(name)::text)
  AND retired_at IS NULL
FOR SHARE;

-- InsertBuilding and InsertSupplierType return no row when a live row with
-- the same lookup key already exists, including one inserted concurrently.
-- Unlike a unique violation this does not abort the caller's transaction, so
-- the caller can resolve again and use the winning row.

-- name: InsertBuilding :one
INSERT INTO buildings (name, created_at)
VALUES (sqlc.arg(name), sqlc.arg(now))
ON CONFLICT (lookup_key) WHERE retired_at IS NULL DO NOTHING
RETURNING id::text AS id, name;

-- name: InsertSupplierType :one
INSERT INTO supplier_types (name, created_at)
VALUES (sqlc.arg(name), sqlc.arg(now))
ON CONFLICT (lookup_key) WHERE retired_at IS NULL DO NOTHING
RETURNING id::text AS id, name;
