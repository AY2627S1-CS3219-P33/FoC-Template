-- Fence, review, audit, settings and reminder queries, owned by Person B and
-- generated into internal/database/opsgen. B reads errands but never writes
-- them, and writes outbox rows only through InsertReminder.

-- name: InsertReminder :execrows
-- InsertReminder writes one reminder event row. A replayed reminder (same
-- idempotency key) affects no rows (NFR7.3). Claiming and sending the row
-- belong to Person C (queries/messaging.sql).
INSERT INTO outbox (kind, idempotency_key, errand_id, payload)
VALUES (@kind, @idempotency_key, @errand_id, @payload)
ON CONFLICT (idempotency_key) DO NOTHING;

-- name: InsertCurrentReminder :execrows
-- InsertCurrentReminder is InsertReminder for the scheduler (B4b): it writes
-- the row only while the errand still has the status and version the
-- reminder was raised for. FOR SHARE waits for a transition in progress on
-- the errand, so a reminder is never written for a state the errand has just
-- left. No row is written for a missing errand either.
INSERT INTO outbox (kind, idempotency_key, errand_id, payload)
SELECT @kind::text, @idempotency_key::text, e.id, @payload::jsonb
FROM errands e
WHERE e.id = @errand_id
  AND e.status = @status
  AND e.version = @version
FOR SHARE OF e
ON CONFLICT (idempotency_key) DO NOTHING;

-- name: ListVerificationReminderCandidates :many
-- ListVerificationReminderCandidates returns DELIVERED errands whose
-- verification deadline is after now and at or before remind_before, and
-- which have no verification reminder for their current version yet
-- (F3.10.3). It reads errands_verification_due_idx; the outbox lookup uses
-- the unique idempotency_key index.
SELECT e.id, e.version, e.status, e.requester_id, e.courier_id,
       e.verification_deadline::timestamptz AS deadline
FROM errands e
WHERE e.status = 'DELIVERED'
  AND e.verification_deadline > @now::timestamptz
  AND e.verification_deadline <= @remind_before::timestamptz
  AND NOT EXISTS (
    SELECT 1 FROM outbox o
    WHERE o.idempotency_key = e.id::text || ':verify-reminder:' || e.version::text
  )
ORDER BY e.verification_deadline, e.id
LIMIT @row_limit;

-- name: ListPickupReminderCandidates :many
-- ListPickupReminderCandidates returns ASSIGNED errands whose pickup
-- deadline is after now and at or before remind_before, and which have no
-- pickup reminder for their current version yet. It reads
-- errands_pickup_due_idx.
SELECT e.id, e.version, e.status, e.requester_id, e.courier_id,
       e.pickup_deadline::timestamptz AS deadline
FROM errands e
WHERE e.status = 'ASSIGNED'
  AND e.pickup_deadline > @now::timestamptz
  AND e.pickup_deadline <= @remind_before::timestamptz
  AND NOT EXISTS (
    SELECT 1 FROM outbox o
    WHERE o.idempotency_key = e.id::text || ':pickup-reminder:' || e.version::text
  )
ORDER BY e.pickup_deadline, e.id
LIMIT @row_limit;

-- name: LockSupplierForFence :exec
-- LockSupplierForFence takes the exclusive advisory lock on the supplier key
-- defined in docs/contracts.md ("Lock key"). Errand creation takes the
-- shared lock on the same key. It must run inside a transaction.
SELECT pg_advisory_xact_lock(hashtextextended('supplier:' || sqlc.arg(supplier_id)::uuid::text, 0));

-- name: InsertFence :execrows
-- InsertFence creates an OPEN fence. Replaying it for an existing operation
-- affects no rows; the caller then reads the row with GetFence.
INSERT INTO supplier_fences (operation_id, supplier_id, state)
VALUES (@operation_id, @supplier_id, 'OPEN')
ON CONFLICT (operation_id) DO NOTHING;

-- name: GetFence :one
SELECT operation_id, supplier_id, state, created_at, updated_at
FROM supplier_fences
WHERE operation_id = @operation_id;

-- name: CloseFence :one
-- CloseFence moves an OPEN fence to COMMITTED or RELEASED. It returns no row
-- when the fence is missing or no longer OPEN; the caller reads it with
-- GetFence to tell the two apart.
UPDATE supplier_fences
SET state = @state, updated_at = clock_timestamp()
WHERE operation_id = @operation_id AND state = 'OPEN'
RETURNING operation_id, supplier_id, state, created_at, updated_at;

-- name: CountActiveErrandsForSupplier :one
-- CountActiveErrandsForSupplier counts errands in one of active_statuses
-- whose supplier, or whose pending edit supplier, is supplier_id
-- (docs/contracts.md, "Fence Acquire" step 3).
SELECT count(*)
FROM errands
WHERE status = ANY (@active_statuses::text[])
  AND (
    supplier_id = @supplier_id::uuid
    OR (pending_edit IS NOT NULL
        AND (pending_edit->>'supplierId')::uuid = @supplier_id::uuid)
  );

-- name: ListOpenReviews :many
-- ListOpenReviews returns unresolved reviews, oldest first, with the errand
-- fields an admin needs to decide. after_id is the last review ID of the
-- previous page, or NULL for the first page.
SELECT
    r.id,
    r.errand_id,
    r.reason,
    r.reporter_id,
    r.note,
    r.created_at,
    e.status,
    e.version,
    e.requester_id,
    e.courier_id,
    e.supplier_id,
    e.reward,
    e.delivery_deadline,
    e.delivered_at,
    e.updated_at AS errand_updated_at
FROM errand_reviews r
JOIN errands e ON e.id = r.errand_id
WHERE r.resolution IS NULL
  AND (sqlc.narg(after_id)::bigint IS NULL OR r.id > sqlc.narg(after_id)::bigint)
ORDER BY r.id
LIMIT @row_limit;

-- name: InsertAudit :one
INSERT INTO admin_audit (actor_id, action, target_type, target_id, reason, outcome)
VALUES (@actor_id, @action, @target_type, @target_id, @reason, @outcome)
RETURNING id, actor_id, action, target_type, target_id, reason, outcome, at;

-- name: ListAudit :many
-- ListAudit returns audit rows newest first. Every filter is optional (NULL
-- matches everything); before_id is the last ID of the previous page.
SELECT id, actor_id, action, target_type, target_id, reason, outcome, at
FROM admin_audit
WHERE (sqlc.narg(actor_id)::text IS NULL OR actor_id = sqlc.narg(actor_id)::text)
  AND (sqlc.narg(action)::text IS NULL OR action = sqlc.narg(action)::text)
  AND (sqlc.narg(target_type)::text IS NULL OR target_type = sqlc.narg(target_type)::text)
  AND (sqlc.narg(target_id)::text IS NULL OR target_id = sqlc.narg(target_id)::text)
  AND (sqlc.narg(outcome)::text IS NULL OR outcome = sqlc.narg(outcome)::text)
  AND (sqlc.narg(since)::timestamptz IS NULL OR at >= sqlc.narg(since)::timestamptz)
  AND (sqlc.narg(until)::timestamptz IS NULL OR at < sqlc.narg(until)::timestamptz)
  AND (sqlc.narg(before_id)::bigint IS NULL OR id < sqlc.narg(before_id)::bigint)
ORDER BY id DESC
LIMIT @row_limit;

-- name: ListSettings :many
SELECT key, value, updated_by, updated_at
FROM settings
ORDER BY key;

-- name: SeedSetting :execrows
-- SeedSetting stores a default. An existing key, seeded or set by an admin,
-- is left as it is.
INSERT INTO settings (key, value)
VALUES (@key, @value)
ON CONFLICT (key) DO NOTHING;

-- name: UpsertSetting :execrows
-- UpsertSetting stores a value set by an admin. A key whose value is unchanged
-- keeps its updated_by and updated_at.
INSERT INTO settings (key, value, updated_by)
VALUES (@key, @value, @updated_by)
ON CONFLICT (key) DO UPDATE
SET value = excluded.value,
    updated_by = excluded.updated_by,
    updated_at = clock_timestamp()
WHERE settings.value IS DISTINCT FROM excluded.value;
