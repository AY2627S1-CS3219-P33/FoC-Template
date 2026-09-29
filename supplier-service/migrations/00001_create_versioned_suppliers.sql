-- +goose Up

-- Keep this function aligned with supplier.NormalizeName: trim outer
-- whitespace, collapse internal whitespace, and lowercase the result.
-- +goose StatementBegin
CREATE FUNCTION normalize_supplier_name(input text)
RETURNS text
LANGUAGE sql
IMMUTABLE
STRICT
PARALLEL SAFE
AS $$
    SELECT lower(btrim(regexp_replace(input, '[[:space:]]+', ' ', 'g')))
$$;
-- +goose StatementEnd

CREATE TABLE suppliers (
    supplier_id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    current_version_id uuid NOT NULL,
    current_normalized_name varchar(120) NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    deleted_at timestamptz,

    CONSTRAINT suppliers_normalized_name_is_canonical CHECK (
        current_normalized_name <> ''
        AND current_normalized_name = normalize_supplier_name(current_normalized_name)
    ),
    CONSTRAINT suppliers_updated_after_creation CHECK (updated_at >= created_at),
    CONSTRAINT suppliers_deleted_after_creation CHECK (
        deleted_at IS NULL OR deleted_at >= created_at
    )
);

-- A partial unique index is the concurrency-safe arbiter for creates and
-- renames. Soft-deleted suppliers release their name for reuse.
CREATE UNIQUE INDEX suppliers_live_normalized_name_uidx
    ON suppliers (current_normalized_name)
    WHERE deleted_at IS NULL;

CREATE TABLE supplier_versions (
    version_id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    supplier_id uuid NOT NULL,
    name varchar(120) NOT NULL,
    normalized_name varchar(120)
        GENERATED ALWAYS AS (normalize_supplier_name(name)) STORED,
    supplier_type varchar(64) NOT NULL,
    building varchar(120) NOT NULL,
    floor varchar(32) NOT NULL,
    location_description varchar(500) NOT NULL,
    latitude double precision NOT NULL,
    longitude double precision NOT NULL,
    opening_time time without time zone NOT NULL,
    closing_time time without time zone NOT NULL,
    image_url varchar(2048),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),

    CONSTRAINT supplier_versions_supplier_fkey
        FOREIGN KEY (supplier_id) REFERENCES suppliers (supplier_id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT supplier_versions_supplier_version_key
        UNIQUE (supplier_id, version_id),
    CONSTRAINT supplier_versions_name_not_blank
        CHECK (normalized_name <> ''),
    CONSTRAINT supplier_versions_type_not_blank
        CHECK (btrim(supplier_type) <> ''),
    CONSTRAINT supplier_versions_building_not_blank
        CHECK (btrim(building) <> ''),
    CONSTRAINT supplier_versions_floor_not_blank
        CHECK (btrim(floor) <> ''),
    CONSTRAINT supplier_versions_location_not_blank
        CHECK (btrim(location_description) <> ''),
    CONSTRAINT supplier_versions_latitude_range
        CHECK (latitude BETWEEN -90 AND 90),
    CONSTRAINT supplier_versions_longitude_range
        CHECK (longitude BETWEEN -180 AND 180),
    CONSTRAINT supplier_versions_hours_differ
        CHECK (opening_time <> closing_time)
);

-- The column order makes it impossible for a supplier's current pointer to
-- reference a version owned by another supplier. It is deferred so create can
-- insert the supplier and its initial immutable version in one transaction.
ALTER TABLE suppliers
    ADD CONSTRAINT suppliers_current_version_fkey
    FOREIGN KEY (supplier_id, current_version_id)
    REFERENCES supplier_versions (supplier_id, version_id)
    ON UPDATE RESTRICT ON DELETE RESTRICT
    DEFERRABLE INITIALLY DEFERRED;

-- +goose StatementBegin
CREATE FUNCTION check_supplier_current_version_name()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    version_normalized_name text;
BEGIN
    SELECT normalized_name
      INTO version_normalized_name
      FROM supplier_versions
     WHERE supplier_id = NEW.supplier_id
       AND version_id = NEW.current_version_id;

    -- A missing row is reported by the deferred composite foreign key.
    IF FOUND AND version_normalized_name <> NEW.current_normalized_name THEN
        RAISE EXCEPTION 'supplier current name does not match its current version'
            USING ERRCODE = '23514',
                  CONSTRAINT = 'suppliers_current_version_name_matches';
    END IF;

    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER suppliers_current_version_name_matches
    AFTER INSERT OR UPDATE OF current_version_id, current_normalized_name
    ON suppliers
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW
    EXECUTE FUNCTION check_supplier_current_version_name();

-- +goose StatementBegin
CREATE FUNCTION reject_immutable_row_change()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION '% rows are immutable', TG_TABLE_NAME
        USING ERRCODE = '55000';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER supplier_versions_are_append_only
    BEFORE UPDATE OR DELETE ON supplier_versions
    FOR EACH ROW
    EXECUTE FUNCTION reject_immutable_row_change();

CREATE TABLE supplier_deletion_operations (
    operation_id uuid PRIMARY KEY,
    supplier_id uuid NOT NULL,
    state varchar(32) NOT NULL DEFAULT 'requested',
    attempt_count integer NOT NULL DEFAULT 0,
    max_attempts integer NOT NULL,
    next_attempt_at timestamptz,
    last_failure_code varchar(32),
    claim_id uuid,
    lease_expires_at timestamptz,
    requested_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    deleted_at timestamptz,
    completed_at timestamptz,

    CONSTRAINT supplier_deletion_operations_supplier_fkey
        FOREIGN KEY (supplier_id) REFERENCES suppliers (supplier_id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT supplier_deletion_operations_state_check CHECK (state IN (
        'requested',
        'release_pending',
        'commit_pending',
        'rejected_active_errands',
        'completed'
    )),
    CONSTRAINT supplier_deletion_operations_attempts_check CHECK (
        max_attempts > 0
        AND attempt_count >= 0
        AND attempt_count <= max_attempts
    ),
    CONSTRAINT supplier_deletion_operations_failure_code_check CHECK (
        last_failure_code IS NULL OR last_failure_code IN (
            'acquire_unavailable',
            'acquire_ambiguous',
            'inspect_unavailable',
            'release_unavailable',
            'commit_unavailable'
        )
    ),
    CONSTRAINT supplier_deletion_operations_lease_pair_check CHECK (
        (claim_id IS NULL) = (lease_expires_at IS NULL)
    ),
    CONSTRAINT supplier_deletion_operations_lease_after_update_check CHECK (
        lease_expires_at IS NULL OR lease_expires_at > updated_at
    ),
    CONSTRAINT supplier_deletion_operations_deleted_state_check CHECK (
        (state IN ('commit_pending', 'completed')) = (deleted_at IS NOT NULL)
    ),
    CONSTRAINT supplier_deletion_operations_completed_state_check CHECK (
        (state = 'completed') = (completed_at IS NOT NULL)
    ),
    CONSTRAINT supplier_deletion_operations_terminal_unclaimed_check CHECK (
        state NOT IN ('rejected_active_errands', 'completed')
        OR (claim_id IS NULL AND next_attempt_at IS NULL)
    ),
    CONSTRAINT supplier_deletion_operations_updated_after_request_check
        CHECK (updated_at >= requested_at),
    CONSTRAINT supplier_deletion_operations_deleted_after_request_check
        CHECK (deleted_at IS NULL OR deleted_at >= requested_at),
    CONSTRAINT supplier_deletion_operations_completed_after_delete_check
        CHECK (completed_at IS NULL OR completed_at >= deleted_at)
);

CREATE INDEX supplier_deletion_operations_claimable_idx
    ON supplier_deletion_operations (next_attempt_at, operation_id)
    WHERE state IN ('requested', 'release_pending', 'commit_pending')
      AND claim_id IS NULL
      AND attempt_count < max_attempts;

CREATE INDEX supplier_deletion_operations_supplier_idx
    ON supplier_deletion_operations (supplier_id, requested_at);

CREATE TABLE supplier_seed_provenance (
    dataset_namespace varchar(120) NOT NULL,
    source_key varchar(120) NOT NULL,
    supplier_id uuid NOT NULL,
    seeded_at timestamptz NOT NULL DEFAULT clock_timestamp(),

    CONSTRAINT supplier_seed_provenance_pkey
        PRIMARY KEY (dataset_namespace, source_key),
    CONSTRAINT supplier_seed_provenance_supplier_key
        UNIQUE (supplier_id),
    CONSTRAINT supplier_seed_provenance_supplier_fkey
        FOREIGN KEY (supplier_id) REFERENCES suppliers (supplier_id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT supplier_seed_provenance_namespace_not_blank
        CHECK (btrim(dataset_namespace) <> ''),
    CONSTRAINT supplier_seed_provenance_source_key_not_blank
        CHECK (btrim(source_key) <> '')
);

CREATE TRIGGER supplier_seed_provenance_is_immutable
    BEFORE UPDATE OR DELETE ON supplier_seed_provenance
    FOR EACH ROW
    EXECUTE FUNCTION reject_immutable_row_change();

-- +goose Down

DROP TABLE IF EXISTS supplier_seed_provenance;
DROP TABLE IF EXISTS supplier_deletion_operations;
ALTER TABLE IF EXISTS suppliers
    DROP CONSTRAINT IF EXISTS suppliers_current_version_fkey;
DROP TABLE IF EXISTS supplier_versions;
DROP TABLE IF EXISTS suppliers;
DROP FUNCTION IF EXISTS reject_immutable_row_change();
DROP FUNCTION IF EXISTS check_supplier_current_version_name();
DROP FUNCTION IF EXISTS normalize_supplier_name(text);
