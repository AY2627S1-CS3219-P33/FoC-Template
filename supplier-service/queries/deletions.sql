-- name: CreateOrGetDeletionOperation :one
INSERT INTO supplier_deletion_operations (
    operation_id,
    supplier_id,
    state,
    max_attempts,
    next_attempt_at,
    requested_at,
    updated_at
) VALUES (
    sqlc.arg(operation_id)::text::uuid,
    sqlc.arg(supplier_id)::text::uuid,
    'requested',
    sqlc.arg(max_attempts),
    sqlc.arg(now),
    sqlc.arg(now),
    sqlc.arg(now)
)
ON CONFLICT (operation_id) DO UPDATE
SET operation_id = supplier_deletion_operations.operation_id
RETURNING
    operation_id::text AS operation_id,
    supplier_id::text AS supplier_id,
    state,
    attempt_count,
    max_attempts,
    next_attempt_at,
    COALESCE(last_failure_code, '') AS last_failure_code,
    COALESCE(claim_id::text, '')::text AS claim_id,
    lease_expires_at,
    requested_at,
    updated_at,
    deleted_at,
    completed_at;

-- name: GetDeletionOperation :one
SELECT
    operation_id::text AS operation_id,
    supplier_id::text AS supplier_id,
    state,
    attempt_count,
    max_attempts,
    next_attempt_at,
    COALESCE(last_failure_code, '') AS last_failure_code,
    COALESCE(claim_id::text, '')::text AS claim_id,
    lease_expires_at,
    requested_at,
    updated_at,
    deleted_at,
    completed_at
FROM supplier_deletion_operations
WHERE operation_id = sqlc.arg(operation_id)::text::uuid;

-- name: ListClaimableDeletionOperations :many
SELECT
    operation_id::text AS operation_id,
    supplier_id::text AS supplier_id,
    state,
    attempt_count,
    max_attempts,
    next_attempt_at,
    COALESCE(last_failure_code, '') AS last_failure_code,
    COALESCE(claim_id::text, '')::text AS claim_id,
    lease_expires_at,
    requested_at,
    updated_at,
    deleted_at,
    completed_at
FROM supplier_deletion_operations
WHERE state IN ('requested', 'release_pending', 'commit_pending')
  AND attempt_count < max_attempts
  AND next_attempt_at IS NOT NULL
  AND next_attempt_at <= sqlc.arg(now)
  AND (claim_id IS NULL OR lease_expires_at <= sqlc.arg(now))
ORDER BY operation_id
LIMIT sqlc.arg(result_limit);

-- name: ClaimDeletionOperation :one
UPDATE supplier_deletion_operations
SET claim_id = sqlc.arg(claim_id)::text::uuid,
    lease_expires_at = sqlc.arg(lease_expires_at),
    attempt_count = attempt_count + 1,
    updated_at = sqlc.arg(now)
WHERE operation_id = sqlc.arg(operation_id)::text::uuid
  AND state IN ('requested', 'release_pending', 'commit_pending')
  AND attempt_count < max_attempts
  AND next_attempt_at IS NOT NULL
  AND next_attempt_at <= sqlc.arg(now)
  AND (claim_id IS NULL OR lease_expires_at <= sqlc.arg(now))
RETURNING
    operation_id::text AS operation_id,
    supplier_id::text AS supplier_id,
    state,
    attempt_count,
    max_attempts,
    next_attempt_at,
    COALESCE(last_failure_code, '') AS last_failure_code,
    claim_id::text AS claim_id,
    lease_expires_at,
    requested_at,
    updated_at,
    deleted_at,
    completed_at;

-- name: MarkDeletionReleasePending :execrows
UPDATE supplier_deletion_operations
SET state = 'release_pending',
    next_attempt_at = sqlc.arg(now),
    last_failure_code = NULL,
    updated_at = sqlc.arg(now)
WHERE operation_id = sqlc.arg(operation_id)::text::uuid
  AND state = 'requested'
  AND claim_id = sqlc.arg(claim_id)::text::uuid
  AND lease_expires_at > sqlc.arg(now);

-- name: MarkDeletionRejectedActiveErrands :execrows
UPDATE supplier_deletion_operations
SET state = 'rejected_active_errands',
    next_attempt_at = NULL,
    last_failure_code = NULL,
    claim_id = NULL,
    lease_expires_at = NULL,
    updated_at = sqlc.arg(now)
WHERE operation_id = sqlc.arg(operation_id)::text::uuid
  AND state = 'release_pending'
  AND claim_id = sqlc.arg(claim_id)::text::uuid
  AND lease_expires_at > sqlc.arg(now);

-- name: SoftDeleteSupplier :execrows
UPDATE suppliers
SET deleted_at = sqlc.arg(now),
    updated_at = sqlc.arg(now)
WHERE supplier_id = sqlc.arg(supplier_id)::text::uuid
  AND deleted_at IS NULL;

-- name: MarkDeletionCommitPending :execrows
UPDATE supplier_deletion_operations
SET state = 'commit_pending',
    next_attempt_at = sqlc.arg(now),
    last_failure_code = NULL,
    deleted_at = sqlc.arg(now),
    updated_at = sqlc.arg(now)
WHERE operation_id = sqlc.arg(operation_id)::text::uuid
  AND supplier_id = sqlc.arg(supplier_id)::text::uuid
  AND state = 'requested'
  AND claim_id = sqlc.arg(claim_id)::text::uuid
  AND lease_expires_at > sqlc.arg(now);

-- name: CompleteDeletionOperation :execrows
UPDATE supplier_deletion_operations
SET state = 'completed',
    next_attempt_at = NULL,
    last_failure_code = NULL,
    claim_id = NULL,
    lease_expires_at = NULL,
    completed_at = sqlc.arg(now),
    updated_at = sqlc.arg(now)
WHERE operation_id = sqlc.arg(operation_id)::text::uuid
  AND state = 'commit_pending'
  AND claim_id = sqlc.arg(claim_id)::text::uuid
  AND lease_expires_at > sqlc.arg(now);

-- name: ScheduleDeletionOperationRetry :execrows
UPDATE supplier_deletion_operations
SET next_attempt_at = sqlc.narg(next_attempt_at),
    last_failure_code = sqlc.arg(last_failure_code),
    claim_id = NULL,
    lease_expires_at = NULL,
    updated_at = sqlc.arg(now)
WHERE operation_id = sqlc.arg(operation_id)::text::uuid
  AND state IN ('requested', 'release_pending', 'commit_pending')
  AND claim_id = sqlc.arg(claim_id)::text::uuid
  AND lease_expires_at > sqlc.arg(now);
