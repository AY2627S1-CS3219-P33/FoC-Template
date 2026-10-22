-- +goose Up

-- Result messages for order-service, written in the same transaction as the
-- balance change that caused them, then published by a worker. The columns
-- follow order-service's outbox so the dispatcher has the same shape.
-- idempotency_key is the result's messageId: a repeated command cannot add a
-- second row.
CREATE TABLE outbox (
    id                bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    idempotency_key   text        NOT NULL,
    routing_key       text        NOT NULL,
    -- The complete message envelope, ready to publish.
    payload           jsonb       NOT NULL,
    status            text        NOT NULL DEFAULT 'PENDING',
    attempts          integer     NOT NULL DEFAULT 0,
    next_attempt_at   timestamptz NOT NULL DEFAULT clock_timestamp(),
    lease_owner       text,
    lease_expires_at  timestamptz,
    last_failure_code text,
    created_at        timestamptz NOT NULL DEFAULT clock_timestamp(),
    sent_at           timestamptz,
    CONSTRAINT outbox_idempotency_key UNIQUE (idempotency_key),
    CONSTRAINT outbox_routing_key_known CHECK (routing_key IN (
        'credit.reserved', 'credit.reservation_refused',
        'credit.adjusted', 'credit.adjust_refused',
        'credit.released', 'credit.transferred', 'credit.operation_failed')),
    CONSTRAINT outbox_status_known CHECK (status IN ('PENDING', 'SENT', 'FAILED')),
    CONSTRAINT outbox_attempts_non_negative CHECK (attempts >= 0)
);

-- Rows due for dispatch.
CREATE INDEX outbox_due_idx ON outbox (next_attempt_at) WHERE status = 'PENDING';

-- +goose Down

DROP TABLE outbox;
