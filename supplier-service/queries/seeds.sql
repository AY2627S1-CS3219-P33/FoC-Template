-- name: LockSeedDataset :exec
SELECT pg_advisory_xact_lock(
    hashtextextended(sqlc.arg(dataset_namespace)::text, 0)
);

-- name: SeedProvenanceExists :one
SELECT EXISTS (
    SELECT 1
    FROM supplier_seed_provenance
    WHERE dataset_namespace = sqlc.arg(dataset_namespace)
      AND source_key = sqlc.arg(source_key)
) AS provenance_exists;

-- name: InsertSeedProvenance :exec
INSERT INTO supplier_seed_provenance (
    dataset_namespace,
    source_key,
    supplier_id,
    seeded_at
) VALUES (
    sqlc.arg(dataset_namespace),
    sqlc.arg(source_key),
    sqlc.arg(supplier_id)::text::uuid,
    sqlc.arg(seeded_at)
);
