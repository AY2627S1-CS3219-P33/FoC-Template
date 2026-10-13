-- +goose Up

-- Keep this function aligned with supplier.LookupKey: remove every character
-- that is not a Unicode letter or an ASCII digit, then lowercase the result.
-- "Com 2", "Com2" and "COM-2" all become "com2". The explicit pg_c_utf8
-- collation (PostgreSQL 17+, UTF-8 database) makes [:alnum:] and lower() use
-- Unicode rules whatever the database locale, so non-ASCII letters are kept
-- and the function is genuinely immutable.
-- +goose StatementBegin
CREATE FUNCTION normalize_lookup_key(input text)
RETURNS text
LANGUAGE sql
IMMUTABLE
STRICT
PARALLEL SAFE
AS $$
    SELECT lower(regexp_replace(input COLLATE pg_c_utf8, '[^[:alnum:]]', '', 'g'))
$$;
-- +goose StatementEnd

-- Lookup names are immutable because every supplier version references its
-- building and type by ID: changing a name in place would rewrite the pickup
-- details of past and active errands (F2.5.1, F2.5.3). The only permitted
-- update is retiring a live row once.
-- +goose StatementBegin
CREATE FUNCTION reject_lookup_change_except_retirement()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    -- Generated columns are not yet computed in a BEFORE trigger, so
    -- lookup_key is excluded; it is derived from the compared name.
    IF OLD.retired_at IS NULL
       AND NEW.retired_at IS NOT NULL
       AND to_jsonb(NEW) - 'retired_at' - 'lookup_key'
           = to_jsonb(OLD) - 'retired_at' - 'lookup_key' THEN
        RETURN NEW;
    END IF;

    RAISE EXCEPTION '% rows are immutable except for retirement', TG_TABLE_NAME
        USING ERRCODE = '55000';
END;
$$;
-- +goose StatementEnd

CREATE TABLE buildings (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name varchar(120) NOT NULL,
    lookup_key text GENERATED ALWAYS AS (normalize_lookup_key(name)) STORED,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    retired_at timestamptz,

    CONSTRAINT buildings_name_not_blank CHECK (btrim(name) <> ''),
    CONSTRAINT buildings_lookup_key_not_blank CHECK (lookup_key <> ''),
    CONSTRAINT buildings_retired_after_creation CHECK (
        retired_at IS NULL OR retired_at >= created_at
    )
);

-- Retired rows release their key so a replacement can be created.
CREATE UNIQUE INDEX buildings_live_lookup_key_uidx
    ON buildings (lookup_key)
    WHERE retired_at IS NULL;

CREATE TRIGGER buildings_only_retire
    BEFORE UPDATE ON buildings
    FOR EACH ROW
    EXECUTE FUNCTION reject_lookup_change_except_retirement();

CREATE TRIGGER buildings_are_never_deleted
    BEFORE DELETE ON buildings
    FOR EACH ROW
    EXECUTE FUNCTION reject_immutable_row_change();

CREATE TABLE supplier_types (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name varchar(64) NOT NULL,
    lookup_key text GENERATED ALWAYS AS (normalize_lookup_key(name)) STORED,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    retired_at timestamptz,

    CONSTRAINT supplier_types_name_not_blank CHECK (btrim(name) <> ''),
    CONSTRAINT supplier_types_lookup_key_not_blank CHECK (lookup_key <> ''),
    CONSTRAINT supplier_types_retired_after_creation CHECK (
        retired_at IS NULL OR retired_at >= created_at
    )
);

CREATE UNIQUE INDEX supplier_types_live_lookup_key_uidx
    ON supplier_types (lookup_key)
    WHERE retired_at IS NULL;

CREATE TRIGGER supplier_types_only_retire
    BEFORE UPDATE ON supplier_types
    FOR EACH ROW
    EXECUTE FUNCTION reject_lookup_change_except_retirement();

CREATE TRIGGER supplier_types_are_never_deleted
    BEFORE DELETE ON supplier_types
    FOR EACH ROW
    EXECUTE FUNCTION reject_immutable_row_change();

-- A historical value with no letter or digit has no lookup key. Fail with a
-- clear message instead of a bare check violation so an operator can decide
-- how to correct it.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM supplier_versions
        WHERE normalize_lookup_key(building) = ''
           OR normalize_lookup_key(supplier_type) = ''
    ) THEN
        RAISE EXCEPTION 'supplier_versions contains a building or supplier_type without a letter or digit'
            USING ERRCODE = '23514';
    END IF;
END;
$$;
-- +goose StatementEnd

-- Backfill canonicalises historical spellings exactly once: every spelling
-- that shares a lookup key ("Com 2" / "Com2", straight / curly apostrophes)
-- becomes one row. Its display name is the most frequent exact spelling,
-- tie-broken by the spelling first used by the earliest version, and its
-- created_at is the first use of any spelling of that key. Versions created
-- before this migration therefore report the canonical name from now on.
INSERT INTO buildings (name, created_at)
SELECT DISTINCT ON (lookup_key)
    spelling,
    min(first_used_at) OVER (PARTITION BY lookup_key)
FROM (
    SELECT
        normalize_lookup_key(building) AS lookup_key,
        building AS spelling,
        count(*) AS uses,
        min(created_at) AS first_used_at
    FROM supplier_versions
    GROUP BY building
) AS spellings
ORDER BY lookup_key, uses DESC, first_used_at, spelling;

INSERT INTO supplier_types (name, created_at)
SELECT DISTINCT ON (lookup_key)
    spelling,
    min(first_used_at) OVER (PARTITION BY lookup_key)
FROM (
    SELECT
        normalize_lookup_key(supplier_type) AS lookup_key,
        supplier_type AS spelling,
        count(*) AS uses,
        min(created_at) AS first_used_at
    FROM supplier_versions
    GROUP BY supplier_type
) AS spellings
ORDER BY lookup_key, uses DESC, first_used_at, spelling;

ALTER TABLE supplier_versions
    ADD COLUMN building_id uuid,
    ADD COLUMN supplier_type_id uuid;

-- Linking existing versions is the one sanctioned rewrite of append-only
-- rows. The trigger is re-enabled inside the same migration transaction, so
-- no other session can observe or exploit the gap.
ALTER TABLE supplier_versions DISABLE TRIGGER supplier_versions_are_append_only;

UPDATE supplier_versions AS v
SET building_id = b.id,
    supplier_type_id = t.id
FROM buildings AS b, supplier_types AS t
WHERE b.lookup_key = normalize_lookup_key(v.building)
  AND t.lookup_key = normalize_lookup_key(v.supplier_type);

ALTER TABLE supplier_versions ENABLE TRIGGER supplier_versions_are_append_only;

ALTER TABLE supplier_versions
    ALTER COLUMN building_id SET NOT NULL,
    ALTER COLUMN supplier_type_id SET NOT NULL,
    ADD CONSTRAINT supplier_versions_building_fkey
        FOREIGN KEY (building_id) REFERENCES buildings (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    ADD CONSTRAINT supplier_versions_supplier_type_fkey
        FOREIGN KEY (supplier_type_id) REFERENCES supplier_types (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    DROP CONSTRAINT supplier_versions_building_not_blank,
    DROP CONSTRAINT supplier_versions_type_not_blank,
    DROP COLUMN building,
    DROP COLUMN supplier_type;

-- +goose Down

-- Rolling back restores free-text columns holding each version's canonical
-- name; the original pre-migration spellings are not recoverable.
ALTER TABLE supplier_versions
    ADD COLUMN supplier_type varchar(64),
    ADD COLUMN building varchar(120);

ALTER TABLE supplier_versions DISABLE TRIGGER supplier_versions_are_append_only;

UPDATE supplier_versions AS v
SET building = b.name,
    supplier_type = t.name
FROM buildings AS b, supplier_types AS t
WHERE b.id = v.building_id
  AND t.id = v.supplier_type_id;

ALTER TABLE supplier_versions ENABLE TRIGGER supplier_versions_are_append_only;

ALTER TABLE supplier_versions
    ALTER COLUMN supplier_type SET NOT NULL,
    ALTER COLUMN building SET NOT NULL,
    ADD CONSTRAINT supplier_versions_type_not_blank
        CHECK (btrim(supplier_type) <> ''),
    ADD CONSTRAINT supplier_versions_building_not_blank
        CHECK (btrim(building) <> ''),
    DROP CONSTRAINT supplier_versions_building_fkey,
    DROP CONSTRAINT supplier_versions_supplier_type_fkey,
    DROP COLUMN building_id,
    DROP COLUMN supplier_type_id;

DROP TABLE IF EXISTS supplier_types;
DROP TABLE IF EXISTS buildings;
DROP FUNCTION IF EXISTS reject_lookup_change_except_retirement();
DROP FUNCTION IF EXISTS normalize_lookup_key(text);
