-- +goose Up

-- User IDs (requester, courier, reporter, resolver, actor) are identity
-- provider subjects, so they are text. Supplier and building IDs are the
-- supplier-service UUIDs; they have no foreign key because that data lives in
-- another service. The status lists below must match the 11 states in
-- references/order-requirements.md.

CREATE TABLE errands (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    status text NOT NULL,
    version integer NOT NULL DEFAULT 1,
    requester_id text NOT NULL,
    courier_id text,
    -- The supplier version active at creation, so pickup details stay
    -- readable after the supplier changes or is deleted (F2.5.1).
    supplier_id uuid NOT NULL,
    supplier_version_id uuid NOT NULL,
    delivery_building_id uuid NOT NULL,
    delivery_building_snapshot jsonb NOT NULL,
    delivery_detail text NOT NULL,
    items jsonb NOT NULL,
    notes text NOT NULL DEFAULT '',
    -- The person the courier contacts, chosen by the requester (NFR3.6.4).
    -- No user-service lookup: the requester may name someone else.
    contact_name text NOT NULL,
    contact_mobile text NOT NULL,
    reward integer NOT NULL,
    acceptance_deadline timestamptz NOT NULL,
    pickup_deadline timestamptz,
    delivery_deadline timestamptz,
    delivered_at timestamptz,
    verification_deadline timestamptz,
    review_reason text,
    create_idempotency_key text NOT NULL,
    -- A reward edit waiting for credit-service: the full edit, the
    -- credit.adjust idempotency key, and when it was requested.
    pending_edit jsonb,
    pending_edit_key text,
    pending_edit_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),

    CONSTRAINT errands_status_known CHECK (status IN (
        'PENDING_RESERVATION', 'OPEN', 'ASSIGNED', 'PICKED_UP', 'DELIVERED',
        'REVIEW_REQUIRED', 'REJECTED', 'CANCELLED', 'EXPIRED', 'COMPLETED',
        'REFUNDED'
    )),
    CONSTRAINT errands_version_positive CHECK (version >= 1),
    CONSTRAINT errands_reward_positive CHECK (reward > 0),
    CONSTRAINT errands_items_not_empty CHECK (
        jsonb_typeof(items) = 'array' AND jsonb_array_length(items) >= 1
    ),
    CONSTRAINT errands_contact_present CHECK (
        btrim(contact_name) <> '' AND btrim(contact_mobile) <> ''
    ),
    CONSTRAINT errands_courier_when_assigned CHECK (
        status NOT IN ('ASSIGNED', 'PICKED_UP', 'DELIVERED', 'REVIEW_REQUIRED')
        OR courier_id IS NOT NULL
    ),
    CONSTRAINT errands_review_reason_known CHECK (
        review_reason IS NULL
        OR review_reason IN ('COURIER_ISSUE', 'REQUESTER_DISPUTE', 'DELIVERY_OVERDUE')
    ),
    CONSTRAINT errands_pending_edit_complete CHECK (
        (pending_edit IS NULL AND pending_edit_key IS NULL AND pending_edit_at IS NULL)
        OR (pending_edit IS NOT NULL AND pending_edit_key IS NOT NULL AND pending_edit_at IS NOT NULL)
    ),
    -- A replayed POST /errands returns the original errand (NFR7.3).
    CONSTRAINT errands_requester_idempotency_key
        UNIQUE (requester_id, create_idempotency_key)
);

-- Open-errand list, filtered by supplier and ordered by deadline (NFR1.3).
CREATE INDEX errands_open_by_supplier_idx
    ON errands (supplier_id, acceptance_deadline)
    WHERE status = 'OPEN';

-- Per-user lists.
CREATE INDEX errands_requester_idx
    ON errands (requester_id, status, created_at DESC);
CREATE INDEX errands_courier_idx
    ON errands (courier_id, status, updated_at DESC);

-- One partial index per deadline job, matching its WHERE clause.
CREATE INDEX errands_acceptance_due_idx
    ON errands (acceptance_deadline)
    WHERE status = 'OPEN';
CREATE INDEX errands_pickup_due_idx
    ON errands (pickup_deadline)
    WHERE status = 'ASSIGNED';
CREATE INDEX errands_delivery_due_idx
    ON errands (delivery_deadline)
    WHERE status = 'PICKED_UP';
CREATE INDEX errands_verification_due_idx
    ON errands (verification_deadline)
    WHERE status = 'DELIVERED';
CREATE INDEX errands_stuck_reservation_idx
    ON errands (created_at)
    WHERE status = 'PENDING_RESERVATION';
CREATE INDEX errands_stuck_edit_idx
    ON errands (pending_edit_at)
    WHERE pending_edit IS NOT NULL;

CREATE TABLE errand_transitions (
    id bigserial PRIMARY KEY,
    errand_id uuid NOT NULL,
    -- NULL for the creation transition into PENDING_RESERVATION.
    from_status text,
    to_status text NOT NULL,
    event text NOT NULL,
    actor_type text NOT NULL,
    -- NULL for SYSTEM transitions.
    actor_id text,
    -- The errand's courier before the transition, or after it when there
    -- was none before (an accept); NULL when it has none. It attributes
    -- forced withdrawals, whose actor is SYSTEM (F3.6.3, F3.6.4).
    courier_id text,
    reason text,
    errand_version integer NOT NULL,
    at timestamptz NOT NULL DEFAULT clock_timestamp(),

    CONSTRAINT errand_transitions_errand_fkey
        FOREIGN KEY (errand_id) REFERENCES errands (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT errand_transitions_from_status_known CHECK (
        from_status IS NULL OR from_status IN (
            'PENDING_RESERVATION', 'OPEN', 'ASSIGNED', 'PICKED_UP', 'DELIVERED',
            'REVIEW_REQUIRED', 'REJECTED', 'CANCELLED', 'EXPIRED', 'COMPLETED',
            'REFUNDED'
        )
    ),
    CONSTRAINT errand_transitions_to_status_known CHECK (to_status IN (
        'PENDING_RESERVATION', 'OPEN', 'ASSIGNED', 'PICKED_UP', 'DELIVERED',
        'REVIEW_REQUIRED', 'REJECTED', 'CANCELLED', 'EXPIRED', 'COMPLETED',
        'REFUNDED'
    )),
    CONSTRAINT errand_transitions_actor_type_known CHECK (
        actor_type IN ('REQUESTER', 'COURIER', 'ADMIN', 'SYSTEM')
    ),
    CONSTRAINT errand_transitions_version_positive CHECK (errand_version >= 1)
);

-- Errand history and the admin event log.
CREATE INDEX errand_transitions_errand_idx
    ON errand_transitions (errand_id, at);
CREATE INDEX errand_transitions_actor_idx
    ON errand_transitions (actor_id, at);
CREATE INDEX errand_transitions_to_status_idx
    ON errand_transitions (to_status, at);
-- Store.CourierHistory: a courier's voluntary and forced withdrawals.
CREATE INDEX errand_transitions_withdrawals_idx
    ON errand_transitions (courier_id, at)
    WHERE event IN ('withdraw', 'pickup_deadline_passed');

-- Credit commands and errand events, written in the same transaction as the
-- state change (F4.6.2, F4.6.3) and kept after failure (NFR7.4.1).
CREATE TABLE outbox (
    id bigserial PRIMARY KEY,
    kind text NOT NULL,
    idempotency_key text NOT NULL,
    errand_id uuid NOT NULL,
    payload jsonb NOT NULL,
    status text NOT NULL DEFAULT 'PENDING',
    attempts integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    lease_owner text,
    lease_expires_at timestamptz,
    last_failure_code text,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    sent_at timestamptz,
    confirmed_at timestamptz,

    CONSTRAINT outbox_idempotency_key UNIQUE (idempotency_key),
    CONSTRAINT outbox_errand_fkey
        FOREIGN KEY (errand_id) REFERENCES errands (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT outbox_kind_known CHECK (
        kind IN ('credit.reserve', 'credit.release', 'credit.transfer', 'credit.adjust')
        OR kind LIKE 'event.%'
    ),
    CONSTRAINT outbox_status_known CHECK (
        status IN ('PENDING', 'SENT', 'CONFIRMED', 'FAILED')
    ),
    -- Only credit commands get a result back from credit-service.
    CONSTRAINT outbox_confirmed_only_credit CHECK (
        status <> 'CONFIRMED' OR kind LIKE 'credit.%'
    ),
    CONSTRAINT outbox_attempts_non_negative CHECK (attempts >= 0)
);

-- Rows due for dispatch.
CREATE INDEX outbox_due_idx
    ON outbox (next_attempt_at)
    WHERE status = 'PENDING';

-- Credit commands sent but not yet confirmed.
CREATE INDEX outbox_unconfirmed_credit_idx
    ON outbox (sent_at)
    WHERE status = 'SENT' AND kind LIKE 'credit.%';

-- Supplier deletion fences (supplier-service docs/contracts.md).
CREATE TABLE supplier_fences (
    operation_id uuid PRIMARY KEY,
    supplier_id uuid NOT NULL,
    state text NOT NULL DEFAULT 'OPEN',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),

    CONSTRAINT supplier_fences_state_known CHECK (
        state IN ('OPEN', 'COMMITTED', 'RELEASED')
    )
);

CREATE INDEX supplier_fences_supplier_idx
    ON supplier_fences (supplier_id);

CREATE TABLE errand_reviews (
    id bigserial PRIMARY KEY,
    errand_id uuid NOT NULL,
    reason text NOT NULL,
    -- NULL when the system raised the review (DELIVERY_OVERDUE).
    reporter_id text,
    note text,
    resolution text,
    resolver_id text,
    resolution_reason text,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    resolved_at timestamptz,

    CONSTRAINT errand_reviews_errand_fkey
        FOREIGN KEY (errand_id) REFERENCES errands (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT errand_reviews_reason_known CHECK (
        reason IN ('COURIER_ISSUE', 'REQUESTER_DISPUTE', 'DELIVERY_OVERDUE')
    ),
    CONSTRAINT errand_reviews_resolution_known CHECK (
        resolution IS NULL OR resolution IN ('COMPLETED', 'REFUNDED')
    ),
    -- A resolution always records who resolved it, why and when (F6.1).
    CONSTRAINT errand_reviews_resolution_complete CHECK (
        (resolution IS NULL AND resolver_id IS NULL
            AND resolution_reason IS NULL AND resolved_at IS NULL)
        OR (resolution IS NOT NULL AND resolver_id IS NOT NULL
            AND resolution_reason IS NOT NULL AND resolved_at IS NOT NULL)
    )
);

-- An errand is in REVIEW_REQUIRED at most once at a time, so it has at most
-- one open review: the row Store.Apply resolves (docs/contracts.md).
CREATE UNIQUE INDEX errand_reviews_open_idx
    ON errand_reviews (errand_id)
    WHERE resolution IS NULL;

CREATE TABLE admin_audit (
    id bigserial PRIMARY KEY,
    actor_id text NOT NULL,
    action text NOT NULL,
    target_type text NOT NULL,
    target_id text NOT NULL,
    reason text,
    outcome text NOT NULL,
    at timestamptz NOT NULL DEFAULT clock_timestamp(),

    CONSTRAINT admin_audit_outcome_known CHECK (
        outcome IN ('SUCCEEDED', 'REJECTED', 'FAILED')
    )
);

-- Deadline defaults and abuse thresholds.
CREATE TABLE settings (
    key text PRIMARY KEY,
    value jsonb NOT NULL,
    -- NULL for seeded defaults.
    updated_by text,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

-- +goose Down

DROP TABLE IF EXISTS settings;
DROP TABLE IF EXISTS admin_audit;
DROP TABLE IF EXISTS errand_reviews;
DROP TABLE IF EXISTS supplier_fences;
DROP TABLE IF EXISTS outbox;
DROP TABLE IF EXISTS errand_transitions;
DROP TABLE IF EXISTS errands;
