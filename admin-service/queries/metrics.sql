-- The dashboard's metrics, computed at query time from the two fact tables
-- (there are no rollups). Every query takes a half-open period
-- [from_at, to_at) and, where it groups by day, the time zone whose midnights
-- bound a day. Definitions match the plan's table: see the comments.

-- name: CountOpenedPerDay :many
-- CountOpenedPerDay is metric 5: errands per day, by the time the errand
-- opened (the errand.opened event), not the time it was created. It reads
-- errand_facts_opened_idx.
SELECT (opened_at AT TIME ZONE @tz::text)::date AS day, count(*) AS created
FROM errand_facts
WHERE opened_at >= @from_at::timestamptz
  AND opened_at < @to_at::timestamptz
GROUP BY day
ORDER BY day;

-- name: SummarizeTimeToAccept :one
-- SummarizeTimeToAccept is metric 6: the first accept minus the opened time,
-- for errands whose first accept falls in the period. A later withdraw and
-- re-accept does not change first_accepted_at, and errands never accepted
-- have none, so they are excluded. The percentiles are 0 when nothing was
-- accepted; the caller reports them as absent then. It reads errand_facts_accepted_idx.
SELECT
    count(*) AS accepted,
    COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY extract(epoch FROM first_accepted_at - opened_at)), 0)::float8 AS median_seconds,
    COALESCE(percentile_cont(0.9) WITHIN GROUP (ORDER BY extract(epoch FROM first_accepted_at - opened_at)), 0)::float8 AS p90_seconds
FROM errand_facts
WHERE first_accepted_at >= @from_at::timestamptz
  AND first_accepted_at < @to_at::timestamptz
  AND opened_at IS NOT NULL;

-- name: CountOutcomes :many
-- CountOutcomes is metric 7: errands that finished (reached a terminal
-- state) in the period, by which of the five terminal statuses they reached.
-- It reads errand_facts_terminal_idx.
SELECT terminal_status, count(*) AS finished
FROM errand_facts
WHERE terminal_at >= @from_at::timestamptz
  AND terminal_at < @to_at::timestamptz
GROUP BY terminal_status;

-- name: RankSuppliers :many
-- RankSuppliers is metric 8: the pickup suppliers with the most errands
-- finished in the period, with how many of those completed. The name is the
-- one on the supplier's most recent finished errand that has one, or empty.
-- Errands
-- not yet enriched have no supplier and are counted by CountUnattributed.
SELECT
    supplier_id,
    COALESCE((array_agg(supplier_name ORDER BY terminal_at DESC) FILTER (WHERE supplier_name IS NOT NULL))[1], '')::text AS name,
    count(*) AS finished,
    count(*) FILTER (WHERE terminal_status = 'COMPLETED') AS completed
FROM errand_facts
WHERE terminal_at >= @from_at::timestamptz
  AND terminal_at < @to_at::timestamptz
  AND supplier_id IS NOT NULL
GROUP BY supplier_id
ORDER BY finished DESC, completed DESC, supplier_id
LIMIT @row_limit;

-- name: RankBuildings :many
-- RankBuildings is metric 9, RankSuppliers for the delivery building.
SELECT
    delivery_building_id AS building_id,
    COALESCE((array_agg(delivery_building_name ORDER BY terminal_at DESC) FILTER (WHERE delivery_building_name IS NOT NULL))[1], '')::text AS name,
    count(*) AS finished,
    count(*) FILTER (WHERE terminal_status = 'COMPLETED') AS completed
FROM errand_facts
WHERE terminal_at >= @from_at::timestamptz
  AND terminal_at < @to_at::timestamptz
  AND delivery_building_id IS NOT NULL
GROUP BY delivery_building_id
ORDER BY finished DESC, completed DESC, delivery_building_id
LIMIT @row_limit;

-- name: CountUnattributed :one
-- CountUnattributed counts errands finished in the period that the rankings
-- leave out because enrichment has not stored their supplier and building yet.
SELECT
    count(*) FILTER (WHERE supplier_id IS NULL) AS without_supplier,
    count(*) FILTER (WHERE delivery_building_id IS NULL) AS without_building
FROM errand_facts
WHERE terminal_at >= @from_at::timestamptz
  AND terminal_at < @to_at::timestamptz;

-- name: CountActiveCouriers :one
-- CountActiveCouriers is metric 10: distinct couriers with at least one
-- accept in the period. It reads transition_facts_type_at_idx.
SELECT count(DISTINCT courier_id) AS couriers
FROM transition_facts
WHERE event_type = 'errand.accepted'
  AND occurred_at >= @from_at::timestamptz
  AND occurred_at < @to_at::timestamptz
  AND courier_id IS NOT NULL;

-- name: SumCompletedRewardPerDay :many
-- SumCompletedRewardPerDay is metric 11: the sum of the reward of COMPLETED
-- errands, by the day they completed. It reads errand_facts_terminal_idx.
SELECT
    (terminal_at AT TIME ZONE @tz::text)::date AS day,
    sum(reward)::bigint AS reward,
    count(*) AS completed
FROM errand_facts
WHERE terminal_status = 'COMPLETED'
  AND terminal_at >= @from_at::timestamptz
  AND terminal_at < @to_at::timestamptz
GROUP BY day
ORDER BY day;
