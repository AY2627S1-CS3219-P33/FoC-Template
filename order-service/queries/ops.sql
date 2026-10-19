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

-- name: CountActiveErrandsForUser :one
-- CountActiveErrandsForUser counts errands in one of active_statuses that
-- user_id requested (credits reserved, F1.2.2) and that are assigned to
-- user_id as courier (F1.2.3), for user-service's account-deletion check.
-- courier_id is cleared when an errand returns to OPEN, so it names only the
-- current courier.
SELECT
    count(*) FILTER (WHERE requester_id = @user_id::text) AS as_requester,
    count(*) FILTER (WHERE courier_id = @user_id::text) AS as_courier
FROM errands
WHERE status = ANY (@active_statuses::text[])
  AND (requester_id = @user_id::text OR courier_id = @user_id::text);

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

-- Admin event log (F6.2.1, F6.2.2). Each query has one indexed access path,
-- chosen by the caller from the most selective filter: errand, then user,
-- then type, then none. Every query is newest first by (at, id). from_at,
-- to_at and the (cursor_at, cursor_id) keyset are always set (-infinity,
-- infinity and (infinity, max id) when the client gave none), so they are
-- index bounds even in a generic plan. Optional residual filters are NULL
-- when unset.

-- name: ListTransitionsByErrand :many
-- ListTransitionsByErrand reads errand_transitions_errand_idx. user_id keeps
-- transitions of an errand the user requested, or that the user caused or
-- was courier of at the time.
SELECT t.id, t.errand_id, t.from_status, t.to_status, t.event, t.actor_type,
       t.actor_id, t.courier_id, t.reason, t.errand_version, t.at
FROM errand_transitions t
WHERE t.errand_id = @errand_id
  AND t.at >= @from_at::timestamptz
  AND t.at < @to_at::timestamptz
  AND (t.at, t.id) < (@cursor_at::timestamptz, @cursor_id::bigint)
  AND (sqlc.narg(to_status)::text IS NULL OR t.to_status = sqlc.narg(to_status)::text)
  AND (sqlc.narg(user_id)::text IS NULL
       OR t.actor_id = sqlc.narg(user_id)::text
       OR t.courier_id = sqlc.narg(user_id)::text
       OR EXISTS (SELECT 1 FROM errands e
                  WHERE e.id = t.errand_id AND e.requester_id = sqlc.narg(user_id)::text))
ORDER BY t.at DESC, t.id DESC
LIMIT @row_limit;

-- name: ListTransitionsByUser :many
-- ListTransitionsByUser returns the transitions of errands user_id requested
-- (errands_requester_idx, then errand_transitions_errand_idx), those the
-- user caused (errand_transitions_actor_idx), and those while the user was
-- the errand's courier. A user only becomes an errand's courier by accepting
-- it, so the last set is found through the user's accept rows
-- (errand_transitions_actor_idx) and then their errands' rows; there is no
-- index on courier_id alone.
SELECT t.id, t.errand_id, t.from_status, t.to_status, t.event, t.actor_type,
       t.actor_id, t.courier_id, t.reason, t.errand_version, t.at
FROM errand_transitions t
WHERE t.id IN (
        SELECT a.id FROM errand_transitions a
        WHERE a.actor_id = @user_id::text
        UNION
        SELECT r.id FROM errands e
        JOIN errand_transitions r ON r.errand_id = e.id
        WHERE e.requester_id = @user_id::text
        UNION
        SELECT c.id FROM errand_transitions c
        WHERE c.courier_id = @user_id::text
          AND c.errand_id IN (SELECT x.errand_id FROM errand_transitions x
                              WHERE x.actor_id = @user_id::text AND x.event = 'accept'))
  AND t.at >= @from_at::timestamptz
  AND t.at < @to_at::timestamptz
  AND (t.at, t.id) < (@cursor_at::timestamptz, @cursor_id::bigint)
  AND (sqlc.narg(to_status)::text IS NULL OR t.to_status = sqlc.narg(to_status)::text)
ORDER BY t.at DESC, t.id DESC
LIMIT @row_limit;

-- name: ListTransitionsByType :many
-- ListTransitionsByType reads errand_transitions_to_status_idx.
SELECT t.id, t.errand_id, t.from_status, t.to_status, t.event, t.actor_type,
       t.actor_id, t.courier_id, t.reason, t.errand_version, t.at
FROM errand_transitions t
WHERE t.to_status = @to_status::text
  AND t.at >= @from_at::timestamptz
  AND t.at < @to_at::timestamptz
  AND (t.at, t.id) < (@cursor_at::timestamptz, @cursor_id::bigint)
ORDER BY t.at DESC, t.id DESC
LIMIT @row_limit;

-- name: ListTransitionsByTime :many
-- ListTransitionsByTime reads errand_transitions_at_idx.
SELECT t.id, t.errand_id, t.from_status, t.to_status, t.event, t.actor_type,
       t.actor_id, t.courier_id, t.reason, t.errand_version, t.at
FROM errand_transitions t
WHERE t.at >= @from_at::timestamptz
  AND t.at < @to_at::timestamptz
  AND (t.at, t.id) < (@cursor_at::timestamptz, @cursor_id::bigint)
ORDER BY t.at DESC, t.id DESC
LIMIT @row_limit;

-- Suspicious-activity counts (F6.3.1, F6.3.2, F3.5.3). Both return the users
-- with at least min_count events at or after since, most events first.

-- name: ListWithdrawalCounts :many
-- ListWithdrawalCounts counts voluntary and forced withdrawals per courier
-- (courier_id attributes forced withdrawals, whose actor is SYSTEM). It reads
-- errand_transitions_withdrawals_idx, whose predicate the WHERE repeats.
SELECT t.courier_id::text AS user_id, count(*) AS event_count
FROM errand_transitions t
WHERE t.event IN ('withdraw', 'pickup_deadline_passed')
  AND t.courier_id IS NOT NULL
  AND t.at >= @since::timestamptz
GROUP BY t.courier_id
HAVING count(*) >= @min_count::bigint
ORDER BY event_count DESC, user_id
LIMIT @row_limit;

-- name: ListCancellationCounts :many
-- ListCancellationCounts counts requester cancellations per requester. Only
-- a requester's cancel enters CANCELLED (Diagram 1), so it reads
-- errand_transitions_to_status_idx.
SELECT t.actor_id::text AS user_id, count(*) AS event_count
FROM errand_transitions t
WHERE t.to_status = 'CANCELLED'
  AND t.event = 'cancel'
  AND t.actor_type = 'REQUESTER'
  AND t.actor_id IS NOT NULL
  AND t.at >= @since::timestamptz
GROUP BY t.actor_id
HAVING count(*) >= @min_count::bigint
ORDER BY event_count DESC, user_id
LIMIT @row_limit;
