-- Outbox delivery queries, generated into internal/database/msggen.

-- name: ClaimOutbox :many
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

-- name: RequeueUnconfirmedOutbox :execrows
UPDATE outbox
SET status = 'PENDING', next_attempt_at = clock_timestamp(),
    lease_owner = NULL, lease_expires_at = NULL
WHERE status = 'SENT' AND kind LIKE 'credit.%'
  AND sent_at <= clock_timestamp() - sqlc.arg(timeout_microseconds)::bigint * interval '1 microsecond';
