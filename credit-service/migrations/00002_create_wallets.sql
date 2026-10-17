-- +goose Up

-- One wallet per user (owner_id is the user-service account ID). Credits are
-- whole numbers. The CHECK constraints are a last line of defence behind the
-- application-level invariants: balances can never go negative.
CREATE TABLE wallets (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id   uuid        NOT NULL,
    available  bigint      NOT NULL DEFAULT 0,
    reserved   bigint      NOT NULL DEFAULT 0,
    version    bigint      NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT wallets_owner_id_key UNIQUE (owner_id),
    CONSTRAINT wallets_available_nonneg CHECK (available >= 0),
    CONSTRAINT wallets_reserved_nonneg CHECK (reserved >= 0)
);

-- Credits held for one order. At most one reservation per order, so editing an
-- order changes the existing row instead of adding another.
CREATE TABLE reservations (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    wallet_id  uuid        NOT NULL REFERENCES wallets (id),
    order_id   uuid        NOT NULL,
    amount     bigint      NOT NULL,
    status     text        NOT NULL DEFAULT 'ACTIVE',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT reservations_order_id_key UNIQUE (order_id),
    CONSTRAINT reservations_amount_positive CHECK (amount > 0),
    CONSTRAINT reservations_status_valid
        CHECK (status IN ('ACTIVE', 'RELEASED', 'TRANSFERRED'))
);

CREATE INDEX reservations_wallet_id_idx ON reservations (wallet_id);

-- Append-only record of every balance change. The deltas let you audit a
-- wallet: available = SUM(available_delta) and reserved = SUM(reserved_delta).
-- (wallet_id, idempotency_key) makes each logical operation apply once.
CREATE TABLE ledger_entries (
    id                     bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    wallet_id              uuid        NOT NULL REFERENCES wallets (id),
    entry_type             text        NOT NULL,
    available_delta        bigint      NOT NULL,
    reserved_delta         bigint      NOT NULL,
    order_id               uuid,
    counterparty_wallet_id uuid        REFERENCES wallets (id),
    idempotency_key        text        NOT NULL,
    created_at             timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ledger_entries_wallet_idempotency_key
        UNIQUE (wallet_id, idempotency_key),
    CONSTRAINT ledger_entries_type_valid CHECK (entry_type IN (
        'ALLOCATE', 'RESERVE', 'RESERVATION_ADJUST', 'RELEASE',
        'TRANSFER_OUT', 'TRANSFER_IN')),
    CONSTRAINT ledger_entries_key_nonempty CHECK (length(idempotency_key) > 0),
    CONSTRAINT ledger_entries_delta_nonzero
        CHECK (available_delta <> 0 OR reserved_delta <> 0)
);

CREATE INDEX ledger_entries_wallet_id_idx ON ledger_entries (wallet_id, id);

-- +goose StatementBegin
CREATE FUNCTION ledger_entries_immutable() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'ledger_entries is append-only';
END
$$;
-- +goose StatementEnd

CREATE TRIGGER ledger_entries_no_change
    BEFORE UPDATE OR DELETE ON ledger_entries
    FOR EACH ROW EXECUTE FUNCTION ledger_entries_immutable();

-- +goose Down

DROP TRIGGER ledger_entries_no_change ON ledger_entries;
DROP FUNCTION ledger_entries_immutable();
DROP TABLE ledger_entries;
DROP TABLE reservations;
DROP TABLE wallets;
