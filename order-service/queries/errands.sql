-- Errand lifecycle queries, owned by Person A and generated into
-- internal/database/errandgen. errandrepo runs them inside its own
-- transactions; none of them commits anything by itself.

-- ---------------------------------------------------------------------------
-- Supplier fence (docs/contracts.md, "Fence and creation locking")
-- ---------------------------------------------------------------------------

-- name: LockSupplierShared :exec
-- LockSupplierShared takes the shared advisory lock on the supplier key
-- defined in docs/contracts.md ("Lock key"). Fence Acquire takes the
-- exclusive lock on the same key. It must run inside a transaction.
SELECT pg_advisory_xact_lock_shared(hashtextextended('supplier:' || sqlc.arg(supplier_id)::uuid::text, 0));

-- name: SupplierFenced :one
-- SupplierFenced reports whether the supplier has an OPEN or COMMITTED
-- fence ("Errand creation" step 2).
SELECT EXISTS (
    SELECT 1
    FROM supplier_fences
    WHERE supplier_id = @supplier_id AND state IN ('OPEN', 'COMMITTED')
)::boolean AS fenced;

-- ---------------------------------------------------------------------------
-- Errand rows
-- ---------------------------------------------------------------------------

-- name: GetErrand :one
SELECT * FROM errands WHERE id = @id;

-- name: GetErrandForUpdate :one
-- GetErrandForUpdate locks the row until the transaction ends, so the
-- compare-and-swap in Store.Apply reads the latest committed errand.
SELECT * FROM errands WHERE id = @id FOR UPDATE;

-- name: GetErrandByCreateKey :one
SELECT *
FROM errands
WHERE requester_id = @requester_id AND create_idempotency_key = @create_idempotency_key;

-- name: InsertErrand :one
-- InsertErrand returns no row when the requester already used the
-- idempotency key; the caller then reads the original with
-- GetErrandByCreateKey (NFR7.3). Any other conflict is an error.
INSERT INTO errands (
    id, status, version, requester_id, courier_id,
    supplier_id, supplier_version_id, delivery_building_id,
    delivery_building_snapshot, delivery_detail, items, notes,
    contact_name, contact_mobile, reward, acceptance_deadline,
    pickup_deadline, delivery_deadline, delivered_at, verification_deadline,
    review_reason, create_idempotency_key,
    pending_edit, pending_edit_key, pending_edit_at,
    created_at, updated_at
) VALUES (
    @id, @status, @version, @requester_id, @courier_id,
    @supplier_id, @supplier_version_id, @delivery_building_id,
    @delivery_building_snapshot, @delivery_detail, @items, @notes,
    @contact_name, @contact_mobile, @reward, @acceptance_deadline,
    @pickup_deadline, @delivery_deadline, @delivered_at, @verification_deadline,
    @review_reason, @create_idempotency_key,
    @pending_edit, @pending_edit_key, @pending_edit_at,
    @created_at, @updated_at
)
ON CONFLICT ON CONSTRAINT errands_requester_idempotency_key DO NOTHING
RETURNING *;

-- name: UpdateErrand :one
-- UpdateErrand writes every mutable column of a transition's result. The
-- WHERE clause is the compare-and-swap: it returns no row unless the errand
-- still has the expected status, version and pending edit key (NULL
-- meaning none).
UPDATE errands
SET status = @status,
    version = @version,
    courier_id = @courier_id,
    supplier_id = @supplier_id,
    supplier_version_id = @supplier_version_id,
    delivery_building_id = @delivery_building_id,
    delivery_building_snapshot = @delivery_building_snapshot,
    delivery_detail = @delivery_detail,
    items = @items,
    notes = @notes,
    contact_name = @contact_name,
    contact_mobile = @contact_mobile,
    reward = @reward,
    acceptance_deadline = @acceptance_deadline,
    pickup_deadline = @pickup_deadline,
    delivery_deadline = @delivery_deadline,
    delivered_at = @delivered_at,
    verification_deadline = @verification_deadline,
    review_reason = @review_reason,
    pending_edit = @pending_edit,
    pending_edit_key = @pending_edit_key,
    pending_edit_at = @pending_edit_at,
    updated_at = @updated_at
WHERE id = @id
  AND status = @expected_status
  AND version = @expected_version
  AND pending_edit_key IS NOT DISTINCT FROM sqlc.narg(expected_pending_edit_key)::text
RETURNING *;

-- ---------------------------------------------------------------------------
-- Rows written with a transition (docs/contracts.md, "Transition and review
-- rows", "Outbox")
-- ---------------------------------------------------------------------------

-- name: InsertTransition :exec
INSERT INTO errand_transitions (
    errand_id, from_status, to_status, event, actor_type, actor_id,
    courier_id, reason, errand_version, at
) VALUES (
    @errand_id, @from_status, @to_status, @event, @actor_type, @actor_id,
    @courier_id, @reason, @errand_version, @at
);

-- name: InsertOutbox :exec
-- InsertOutbox fails on an existing idempotency key, which aborts the
-- transaction: a transition never emits a key twice.
INSERT INTO outbox (kind, idempotency_key, errand_id, payload)
VALUES (@kind, @idempotency_key, @errand_id, @payload);

-- name: InsertOutboxIfAbsent :execrows
-- InsertOutboxIfAbsent is for rejection rows: a retried request with the
-- same key affects no rows.
INSERT INTO outbox (kind, idempotency_key, errand_id, payload)
VALUES (@kind, @idempotency_key, @errand_id, @payload)
ON CONFLICT (idempotency_key) DO NOTHING;

-- name: OpenReview :exec
INSERT INTO errand_reviews (errand_id, reason, reporter_id, note, created_at)
VALUES (@errand_id, @reason, @reporter_id, @note, @created_at);

-- name: ResolveReview :execrows
-- ResolveReview resolves the errand's open review. It affects no rows when
-- the errand has none, which the caller treats as a bug.
UPDATE errand_reviews
SET resolution = @resolution,
    resolver_id = @resolver_id,
    resolution_reason = @resolution_reason,
    resolved_at = @resolved_at
WHERE errand_id = @errand_id AND resolution IS NULL;

-- ---------------------------------------------------------------------------
-- Lists. Each fetches one row more than the page so the caller knows
-- whether another page follows.
-- ---------------------------------------------------------------------------

-- name: ListOpen :many
SELECT *
FROM errands
WHERE status = 'OPEN'
  AND acceptance_deadline > @now
  AND (sqlc.narg(supplier_id)::uuid IS NULL OR supplier_id = sqlc.narg(supplier_id)::uuid)
  AND (sqlc.narg(after_deadline)::timestamptz IS NULL
       OR (acceptance_deadline, id) > (sqlc.narg(after_deadline)::timestamptz, sqlc.narg(after_id)::uuid))
ORDER BY acceptance_deadline, id
LIMIT @row_limit;

-- name: ListRequested :many
SELECT *
FROM errands
WHERE requester_id = @user_id
  AND status = ANY (@statuses::text[])
  AND (sqlc.narg(after_at)::timestamptz IS NULL
       OR (created_at, id) < (sqlc.narg(after_at)::timestamptz, sqlc.narg(after_id)::uuid))
ORDER BY created_at DESC, id DESC
LIMIT @row_limit;

-- name: ListDelivering :many
SELECT *
FROM errands
WHERE courier_id = @user_id
  AND status = ANY (@statuses::text[])
  AND (sqlc.narg(after_at)::timestamptz IS NULL
       OR (updated_at, id) < (sqlc.narg(after_at)::timestamptz, sqlc.narg(after_id)::uuid))
ORDER BY updated_at DESC, id DESC
LIMIT @row_limit;

-- name: ListHistory :many
-- A NULL role keeps errands the user requested or delivered.
SELECT *
FROM errands
WHERE ((sqlc.narg(role)::text IS NULL AND (requester_id = @user_id OR courier_id = @user_id))
       OR (sqlc.narg(role)::text = 'REQUESTER' AND requester_id = @user_id)
       OR (sqlc.narg(role)::text = 'COURIER' AND courier_id = @user_id))
  AND status = ANY (@statuses::text[])
  AND (sqlc.narg(after_at)::timestamptz IS NULL
       OR (updated_at, id) < (sqlc.narg(after_at)::timestamptz, sqlc.narg(after_id)::uuid))
ORDER BY updated_at DESC, id DESC
LIMIT @row_limit;

-- name: CourierHistory :one
-- CourierHistory counts courier_id's voluntary and forced withdrawals
-- (errand_transitions_withdrawals_idx). A NULL errand_id matches no row.
SELECT
    coalesce(bool_or(errand_id = sqlc.narg(errand_id)::uuid), false)::boolean AS withdrew_from_errand,
    (count(*) FILTER (WHERE at >= @since))::integer AS withdrawals_since
FROM errand_transitions
WHERE courier_id = @courier_id
  AND event IN ('withdraw', 'pickup_deadline_passed');

-- ---------------------------------------------------------------------------
-- ClaimDue. Each WHERE clause is its partial index predicate plus
-- "column <= now" (errand.DueKind).
-- ---------------------------------------------------------------------------

-- name: ClaimAcceptanceExpiry :many
SELECT * FROM errands
WHERE status = 'OPEN' AND acceptance_deadline <= @now
ORDER BY acceptance_deadline, id
LIMIT @row_limit
FOR UPDATE SKIP LOCKED;

-- name: ClaimPickupDeadline :many
SELECT * FROM errands
WHERE status = 'ASSIGNED' AND pickup_deadline <= @now
ORDER BY pickup_deadline, id
LIMIT @row_limit
FOR UPDATE SKIP LOCKED;

-- name: ClaimDeliveryDeadline :many
SELECT * FROM errands
WHERE status = 'PICKED_UP' AND delivery_deadline <= @now
ORDER BY delivery_deadline, id
LIMIT @row_limit
FOR UPDATE SKIP LOCKED;

-- name: ClaimVerificationLapse :many
SELECT * FROM errands
WHERE status = 'DELIVERED' AND verification_deadline <= @now
ORDER BY verification_deadline, id
LIMIT @row_limit
FOR UPDATE SKIP LOCKED;

-- name: ClaimStuckReservation :many
SELECT * FROM errands
WHERE status = 'PENDING_RESERVATION' AND created_at <= @now
ORDER BY created_at, id
LIMIT @row_limit
FOR UPDATE SKIP LOCKED;

-- name: ClaimStuckEdit :many
SELECT * FROM errands
WHERE pending_edit IS NOT NULL AND pending_edit_at <= @now
ORDER BY pending_edit_at, id
LIMIT @row_limit
FOR UPDATE SKIP LOCKED;
