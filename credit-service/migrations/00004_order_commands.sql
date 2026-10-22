-- +goose Up

-- Commands from order-service can arrive more than once. This table keeps the
-- result of every command that was applied, keyed by the command's message ID,
-- so a repeat gets the original result and changes no balance. A refusal is
-- stored too: a repeat must not succeed just because balances have changed.
CREATE TABLE processed_commands (
    message_id  text        PRIMARY KEY,
    result_type text        NOT NULL,
    result      jsonb       NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT processed_commands_message_id_nonempty CHECK (length(message_id) > 0)
);

-- A release can overtake the reserve it belongs to. The release leaves a
-- tombstone, and a reserve for the same order that arrives later is refused.
CREATE TABLE released_orders (
    order_id   uuid        PRIMARY KEY,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Highest adjust seq applied to the reservation. An adjust that is not newer
-- is a late or repeated one and changes nothing.
ALTER TABLE reservations
    ADD COLUMN last_adjust_seq bigint NOT NULL DEFAULT 0;

-- +goose Down

ALTER TABLE reservations DROP COLUMN last_adjust_seq;
DROP TABLE released_orders;
DROP TABLE processed_commands;
