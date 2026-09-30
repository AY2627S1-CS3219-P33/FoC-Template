package readiness

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

var requiredTables = []string{
	"suppliers",
	"supplier_versions",
	"supplier_deletion_operations",
	"supplier_seed_provenance",
}

// PostgresChecker verifies the minimum durable state needed by supplier
// operations. Seed imports are atomic, so provenance for the configured
// namespace proves that the required dataset was completely applied.
type PostgresChecker struct {
	pool             *pgxpool.Pool
	datasetNamespace string
}

func NewPostgresChecker(pool *pgxpool.Pool, datasetNamespace string) *PostgresChecker {
	if pool == nil {
		panic("readiness: PostgreSQL pool is required")
	}
	datasetNamespace = strings.TrimSpace(datasetNamespace)
	if datasetNamespace == "" {
		panic("readiness: seed dataset namespace is required")
	}
	return &PostgresChecker{pool: pool, datasetNamespace: datasetNamespace}
}

func (c *PostgresChecker) Check(ctx context.Context) error {
	if err := c.pool.Ping(ctx); err != nil {
		return errors.New("database is unavailable")
	}

	for _, table := range requiredTables {
		var exists bool
		if err := c.pool.QueryRow(ctx, `
			SELECT to_regclass($1) IS NOT NULL
		`, table).Scan(&exists); err != nil {
			return errors.New("database schema could not be verified")
		}
		if !exists {
			return errors.New("required database schema is missing")
		}
	}
	if _, err := c.pool.Exec(ctx, `
		SELECT s.supplier_id,
		       s.current_version_id,
		       v.version_id,
		       d.operation_id,
		       d.state,
		       p.dataset_namespace,
		       p.source_key
		FROM suppliers AS s
		LEFT JOIN supplier_versions AS v ON false
		LEFT JOIN supplier_deletion_operations AS d ON false
		LEFT JOIN supplier_seed_provenance AS p ON false
		WHERE false
	`); err != nil {
		return errors.New("required database schema is incomplete")
	}

	var seeded bool
	if err := c.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM supplier_seed_provenance
			WHERE dataset_namespace = $1
		)
	`, c.datasetNamespace).Scan(&seeded); err != nil {
		return errors.New("seed state could not be verified")
	}
	if !seeded {
		return errors.New("required seed dataset is missing")
	}
	return nil
}
