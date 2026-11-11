-- AI Assistance Disclosure:
-- Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-09
-- Scope: Implementation of SQL migration scripts for the team-designed schema.
-- Author review: Reviewed, validated, and tested by the authors.

-- +goose Up

-- Saved delivery and pickup places, used only to fill in errand requests
-- (references/requester-enhancements-n2h.md). user_id is the identity
-- provider subject. Supplier and building IDs are supplier-service UUIDs with
-- no foreign key, so a place can go stale: it is marked unavailable when read,
-- never deleted here. Contact details are not saved (NFR3.6.4).

CREATE TABLE saved_places (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id text NOT NULL,
    kind text NOT NULL,
    label text NOT NULL,
    -- PICKUP only.
    supplier_id uuid,
    -- DELIVERY only.
    delivery_building_id uuid,
    delivery_detail text,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),

    CONSTRAINT saved_places_kind_known CHECK (kind IN ('DELIVERY', 'PICKUP')),
    CONSTRAINT saved_places_label_present CHECK (
        btrim(label) <> '' AND char_length(label) <= 40
    ),
    CONSTRAINT saved_places_kind_columns CHECK (
        (kind = 'DELIVERY' AND supplier_id IS NULL
            AND delivery_building_id IS NOT NULL AND delivery_detail IS NOT NULL)
        OR (kind = 'PICKUP' AND supplier_id IS NOT NULL
            AND delivery_building_id IS NULL AND delivery_detail IS NULL)
    )
);

-- Labels are unique per user and kind, ignoring case.
CREATE UNIQUE INDEX saved_places_label_idx
    ON saved_places (user_id, kind, lower(label));

-- A user saves a supplier at most once.
CREATE UNIQUE INDEX saved_places_supplier_idx
    ON saved_places (user_id, supplier_id)
    WHERE kind = 'PICKUP';

-- Per-user lists, newest first, and the per-kind limit count.
CREATE INDEX saved_places_user_idx
    ON saved_places (user_id, kind, created_at);

-- +goose Down

DROP TABLE IF EXISTS saved_places;
