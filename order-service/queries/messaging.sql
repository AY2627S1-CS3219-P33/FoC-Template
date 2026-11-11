-- AI Assistance Disclosure:
-- Tool: Claude Code (model: Claude Opus 5.5), date: 2026-10-18
-- Scope: Implementation of SQL queries.
-- Author review: Reviewed, validated, and tested by the authors.

-- Outbox delivery queries, generated into internal/database/msggen.

-- name: ClaimOutbox :many
-- Uses outbox_due_idx. Credit and event rows are claimed by separate loops so
-- an event backlog never delays credit commands.
WITH due AS (
    SELECT id FROM outbox
    WHERE status = 'PENDING'
      AND next_attempt_at <= clock_timestamp()
      AND (lease_expires_at IS NULL OR lease_expires_at <= clock_timestamp())
      AND ((sqlc.arg(credit)::boolean AND kind LIKE 'credit.%')
        OR (NOT sqlc.arg(credit)::boolean AND kind LIKE 'event.%'))
    ORDER BY next_attempt_at, id
    LIMIT sqlc.arg(batch_size)
    FOR UPDATE SKIP LOCKED
)
UPDATE outbox AS o
SET lease_owner = sqlc.arg(worker),
    lease_expires_at = clock_timestamp() + sqlc.arg(lease_microseconds)::bigint * interval '1 microsecond',
    attempts = o.attempts + 1
FROM due WHERE o.id = due.id
RETURNING o.*;

-- name: MarkOutboxSent :execrows
UPDATE outbox
SET status = 'SENT', sent_at = clock_timestamp(),
    lease_owner = NULL, lease_expires_at = NULL, last_failure_code = NULL
WHERE id = sqlc.arg(id) AND status = 'PENDING'
  AND lease_owner = sqlc.arg(worker) AND attempts = sqlc.arg(attempts)
  AND lease_expires_at > clock_timestamp();

-- name: RescheduleOutbox :execrows
UPDATE outbox
SET next_attempt_at = clock_timestamp() + sqlc.arg(delay_microseconds)::bigint * interval '1 microsecond',
    lease_owner = NULL, lease_expires_at = NULL, last_failure_code = sqlc.arg(failure_code)
WHERE id = sqlc.arg(id) AND status = 'PENDING'
  AND lease_owner = sqlc.arg(worker) AND attempts = sqlc.arg(attempts)
  AND lease_expires_at > clock_timestamp();

-- name: MarkOutboxFailed :execrows
UPDATE outbox
SET status = 'FAILED', lease_owner = NULL, lease_expires_at = NULL,
    last_failure_code = sqlc.arg(failure_code)
WHERE id = sqlc.arg(id) AND status = 'PENDING'
  AND lease_owner = sqlc.arg(worker) AND attempts = sqlc.arg(attempts)
  AND lease_expires_at > clock_timestamp();

-- name: ConfirmOutboxByKey :execrows
-- A fast result can arrive before MarkOutboxSent. Never let that later write
-- move a terminal result back to SENT.
UPDATE outbox
SET status = 'CONFIRMED', confirmed_at = clock_timestamp(),
    lease_owner = NULL, lease_expires_at = NULL, last_failure_code = NULL
WHERE idempotency_key = sqlc.arg(idempotency_key) AND kind LIKE 'credit.%'
  AND status IN ('PENDING', 'SENT');

-- name: FailOutboxByKey :execrows
-- credit.operation_failed: the command can never succeed, so an admin must
-- look at it. A CONFIRMED row stays confirmed.
UPDATE outbox
SET status = 'FAILED', lease_owner = NULL, lease_expires_at = NULL,
    last_failure_code = sqlc.arg(failure_code)
WHERE idempotency_key = sqlc.arg(idempotency_key) AND kind LIKE 'credit.%'
  AND status IN ('PENDING', 'SENT');

-- name: GetOutboxByKey :one
SELECT * FROM outbox WHERE idempotency_key = sqlc.arg(idempotency_key);

-- name: GetOutboxStatus :one
SELECT status FROM outbox WHERE id = sqlc.arg(id);

-- name: ListFailedOutbox :many
-- Oldest first; after_id is the last ID of the previous page.
SELECT * FROM outbox
WHERE status = 'FAILED' AND id > sqlc.arg(after_id)
ORDER BY id
LIMIT sqlc.arg(row_limit);

-- name: RequeueOutbox :one
-- Admin replay (NFR7.4.2): same key, attempts reset, due now. Only a FAILED
-- row or a SENT credit command can be replayed; a SENT event is delivered
-- already, and a PENDING row is still being retried by the dispatcher.
UPDATE outbox
SET status = 'PENDING', attempts = 0, next_attempt_at = clock_timestamp(),
    lease_owner = NULL, lease_expires_at = NULL
WHERE id = sqlc.arg(id)
  AND (status = 'FAILED' OR (status = 'SENT' AND kind LIKE 'credit.%'))
RETURNING *;

-- name: ListOverdueOutbox :many
-- Rows that have waited longer than the threshold: SENT credit commands with
-- no result yet (outbox_unconfirmed_credit_idx) and PENDING rows the
-- dispatcher has not managed to publish. Event rows end at SENT by design, so
-- they are never overdue. status_filter is empty for both. Oldest ID first;
-- after_id is the last ID of the previous page.
SELECT * FROM outbox
WHERE id > sqlc.arg(after_id)
  AND (sqlc.arg(status_filter)::text = '' OR status = sqlc.arg(status_filter)::text)
  AND (
    (status = 'SENT' AND kind LIKE 'credit.%'
      AND sent_at <= clock_timestamp() - sqlc.arg(threshold_microseconds)::bigint * interval '1 microsecond')
    OR (status = 'PENDING'
      AND created_at <= clock_timestamp() - sqlc.arg(threshold_microseconds)::bigint * interval '1 microsecond')
  )
ORDER BY id
LIMIT sqlc.arg(row_limit);

-- name: InsertRequeueAudit :one
INSERT INTO admin_audit (actor_id, action, target_type, target_id, reason, outcome)
VALUES (sqlc.arg(actor_id), 'OUTBOX_REQUEUE', 'OUTBOX', sqlc.arg(target_id), sqlc.arg(reason), sqlc.arg(outcome))
RETURNING id;
