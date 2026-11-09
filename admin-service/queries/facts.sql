-- The read model's writes and the reads enrichment needs. The metric queries
-- are in metrics.sql.

-- name: InsertTransitionFact :execrows
-- InsertTransitionFact records one event. A message_id already stored (a
-- redelivery, or the backfill loading an event the stream already brought)
-- affects no rows.
INSERT INTO transition_facts (
    message_id, errand_id, errand_version, event_type, from_status, to_status,
    courier_id, reward, occurred_at
) VALUES (
    @message_id, @errand_id, @errand_version, @event_type, sqlc.narg(from_status),
    @to_status, sqlc.narg(courier_id), @reward, @occurred_at
)
ON CONFLICT (message_id) DO NOTHING;

-- name: InsertErrandFact :execrows
-- InsertErrandFact creates the errand's row from its first event. An errand
-- that already has a row affects no rows; the caller then merges into it.
INSERT INTO errand_facts (
    errand_id, status, version, last_event_at, courier_id, reward,
    opened_at, first_accepted_at, terminal_status, terminal_at, enrichment_due
) VALUES (
    @errand_id, @status, @version, @last_event_at, sqlc.narg(courier_id), @reward,
    sqlc.narg(opened_at), sqlc.narg(first_accepted_at), sqlc.narg(terminal_status),
    sqlc.narg(terminal_at), @enrichment_due
)
ON CONFLICT (errand_id) DO NOTHING;

-- name: LockErrandFact :one
-- LockErrandFact reads the errand's row and holds it until the transaction
-- ends, so two consumers merging events of one errand take turns.
SELECT errand_id, status, version, last_event_at, courier_id, reward,
       opened_at, first_accepted_at, terminal_status, terminal_at, enrichment_due
FROM errand_facts
WHERE errand_id = @errand_id
FOR UPDATE;

-- name: UpdateErrandFact :exec
UPDATE errand_facts
SET status = @status,
    version = @version,
    last_event_at = @last_event_at,
    courier_id = sqlc.narg(courier_id),
    reward = @reward,
    opened_at = sqlc.narg(opened_at),
    first_accepted_at = sqlc.narg(first_accepted_at),
    terminal_status = sqlc.narg(terminal_status),
    terminal_at = sqlc.narg(terminal_at),
    enrichment_due = @enrichment_due
WHERE errand_id = @errand_id;

-- name: ListEnrichmentDue :many
-- ListEnrichmentDue returns errands that owe an enrichment: never tried, or
-- last tried before retry_before (enriched_at is when enrichment last ran).
-- It reads errand_facts_enrichment_due_idx.
SELECT errand_id
FROM errand_facts
WHERE enrichment_due
  AND (enriched_at IS NULL OR enriched_at < @retry_before::timestamptz)
ORDER BY last_event_at
LIMIT @row_limit;

-- name: StoreEnrichment :execrows
-- StoreEnrichment replaces the supplier and building with what order-service
-- exported, so the id and its name always belong together. The errand stays
-- due while the supplier name is unknown (supplier-service was unavailable);
-- an unknown building name is not retried, because it does not depend on
-- another service.
UPDATE errand_facts
SET supplier_id = sqlc.narg(supplier_id),
    supplier_name = sqlc.narg(supplier_name),
    delivery_building_id = sqlc.narg(delivery_building_id),
    delivery_building_name = sqlc.narg(delivery_building_name),
    enriched_at = @enriched_at,
    enrichment_due = (sqlc.narg(supplier_name)::text IS NULL)
WHERE errand_id = @errand_id;

-- name: MarkEnrichmentTried :execrows
-- MarkEnrichmentTried records that enrichment ran for an errand order-service
-- did not return, without touching what is stored, so it is asked about
-- again only after the retry time.
UPDATE errand_facts
SET enriched_at = @enriched_at
WHERE errand_id = @errand_id;
