-- +goose Up
-- admin-service's read model: facts distilled from order-service's events.
-- There are no rollup tables; the metrics are computed at query time from
-- these two tables (R2-3). Names and statuses mirror order-service's.

-- One row per event order-service published. message_id is the event's
-- messageId ("{errandId}:{event}:{version}"), so a redelivered event and the
-- same event loaded by the backfill are the same row.
CREATE TABLE transition_facts (
    message_id text PRIMARY KEY,
    errand_id uuid NOT NULL,
    errand_version integer NOT NULL,
    -- The routing key, for example 'errand.accepted'.
    event_type text NOT NULL,
    -- NULL for the creation transition.
    from_status text,
    to_status text NOT NULL,
    -- The courier involved; NULL when the event names none.
    courier_id text,
    reward integer NOT NULL,
    occurred_at timestamptz NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),

    CONSTRAINT transition_facts_version_positive CHECK (errand_version >= 1),
    CONSTRAINT transition_facts_from_status_known CHECK (
        from_status IS NULL OR from_status IN (
            'PENDING_RESERVATION', 'OPEN', 'ASSIGNED', 'PICKED_UP', 'DELIVERED',
            'REVIEW_REQUIRED', 'REJECTED', 'CANCELLED', 'EXPIRED', 'COMPLETED',
            'REFUNDED'
        )
    ),
    CONSTRAINT transition_facts_to_status_known CHECK (to_status IN (
        'PENDING_RESERVATION', 'OPEN', 'ASSIGNED', 'PICKED_UP', 'DELIVERED',
        'REVIEW_REQUIRED', 'REJECTED', 'CANCELLED', 'EXPIRED', 'COMPLETED',
        'REFUNDED'
    ))
);

-- Active couriers (distinct couriers with an accept in a period) and the
-- accept lookups read the event type by time.
CREATE INDEX transition_facts_type_at_idx
    ON transition_facts (event_type, occurred_at);
-- The facts of one errand, in order.
CREATE INDEX transition_facts_errand_idx
    ON transition_facts (errand_id, errand_version);

-- One row per errand: its latest known state and the times the metrics need.
CREATE TABLE errand_facts (
    errand_id uuid PRIMARY KEY,
    status text NOT NULL,
    -- The highest errandVersion applied. An event with a lower version is
    -- stale; an equal one (the withdrawn/expired pair) is ordered by
    -- last_event_at.
    version integer NOT NULL,
    -- occurred_at of the event that set status and version.
    last_event_at timestamptz NOT NULL,
    courier_id text,
    reward integer NOT NULL,
    -- Supplier and building come from order-service's export, not from the
    -- events (enrichment, R2-4). They stay NULL until it has run.
    supplier_id uuid,
    supplier_name text,
    delivery_building_id uuid,
    delivery_building_name text,
    -- The errand.opened event time: PENDING_RESERVATION to OPEN. Not the
    -- time the errand was created, which includes the credit-reservation wait.
    opened_at timestamptz,
    -- The earliest accept. A later withdraw and re-accept does not change it.
    first_accepted_at timestamptz,
    -- Set by the terminal event: COMPLETED, REFUNDED, REJECTED, CANCELLED
    -- or EXPIRED.
    terminal_status text,
    terminal_at timestamptz,
    -- When enrichment last ran for this errand, whether or not it found
    -- everything; an errand still due is retried once this is old enough.
    enriched_at timestamptz,
    -- True after an accepted or terminal event until enrichment has stored
    -- both names, so a null supplier name is retried later.
    enrichment_due boolean NOT NULL DEFAULT false,

    CONSTRAINT errand_facts_status_known CHECK (status IN (
        'PENDING_RESERVATION', 'OPEN', 'ASSIGNED', 'PICKED_UP', 'DELIVERED',
        'REVIEW_REQUIRED', 'REJECTED', 'CANCELLED', 'EXPIRED', 'COMPLETED',
        'REFUNDED'
    )),
    CONSTRAINT errand_facts_version_positive CHECK (version >= 1),
    CONSTRAINT errand_facts_terminal_known CHECK (
        terminal_status IS NULL OR terminal_status IN (
            'COMPLETED', 'REFUNDED', 'REJECTED', 'CANCELLED', 'EXPIRED'
        )
    ),
    CONSTRAINT errand_facts_terminal_complete CHECK (
        (terminal_status IS NULL) = (terminal_at IS NULL)
    )
);

-- Errands per day, by opened_at.
CREATE INDEX errand_facts_opened_idx
    ON errand_facts (opened_at)
    WHERE opened_at IS NOT NULL;
-- Time to accept: errands whose first accept falls in the period.
CREATE INDEX errand_facts_accepted_idx
    ON errand_facts (first_accepted_at)
    WHERE first_accepted_at IS NOT NULL;
-- Outcome rates, reward volume and the rankings: errands finished in the
-- period. The INCLUDE columns let the ranking read the index alone.
CREATE INDEX errand_facts_terminal_idx
    ON errand_facts (terminal_at, terminal_status)
    INCLUDE (supplier_id, delivery_building_id, reward)
    WHERE terminal_at IS NOT NULL;
-- Rows enrichment still owes, oldest first.
CREATE INDEX errand_facts_enrichment_due_idx
    ON errand_facts (last_event_at)
    WHERE enrichment_due;

-- The service connects as the runtime role admin_service, which may read and
-- write rows but owns nothing and runs no DDL. The role is created outside
-- migrations (compose's db-init script locally, docs/ecs-deployment-notes.md
-- in AWS), so skip the grant where it does not exist: an environment without
-- it, such as a test database, still migrates.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'admin_service') THEN
        GRANT SELECT, INSERT, UPDATE ON transition_facts, errand_facts TO admin_service;
    END IF;
END
$$;
-- +goose StatementEnd

-- +goose Down
DROP TABLE IF EXISTS errand_facts;
DROP TABLE IF EXISTS transition_facts;
