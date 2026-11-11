-- AI Assistance Disclosure:
-- Tool: Claude, date: 2026-11-11
-- Scope: Database schema migrations for notification-service.
-- Author review: I validated correctness, edited for style, and added boundary checks.

-- +goose Up

-- One row per (event, recipient). A single errand event can notify both the
-- requester and the courier, so each gets their own row. recipient is an Auth0
-- subject (not a UUID). created_at is the event's occurredAt so the order a
-- user sees matches the order things happened; inserted_at records when the
-- row actually landed.
CREATE TABLE notifications (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    message_id  text        NOT NULL,
    recipient   text        NOT NULL,
    errand_id   uuid        NOT NULL,
    type        text        NOT NULL,
    title       text        NOT NULL,
    body        text        NOT NULL,
    created_at  timestamptz NOT NULL,
    read_at     timestamptz,
    inserted_at timestamptz NOT NULL DEFAULT now(),
    -- At-least-once delivery means an event can arrive more than once; this
    -- makes storing it idempotent per recipient.
    CONSTRAINT notifications_message_recipient_key UNIQUE (message_id, recipient),
    CONSTRAINT notifications_recipient_nonempty CHECK (length(recipient) > 0),
    CONSTRAINT notifications_message_id_nonempty CHECK (length(message_id) > 0)
);

-- Lists a recipient's notifications newest first; (created_at, id) is the
-- stable total order the keyset cursor resumes from.
CREATE INDEX notifications_recipient_created_idx
    ON notifications (recipient, created_at DESC, id DESC);

-- Counts and lists only the unread ones without scanning the read history.
CREATE INDEX notifications_recipient_unread_idx
    ON notifications (recipient, created_at DESC, id DESC)
    WHERE read_at IS NULL;

-- +goose Down

DROP TABLE notifications;
