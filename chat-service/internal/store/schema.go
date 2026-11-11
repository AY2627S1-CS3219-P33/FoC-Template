// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-10
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SchemaCheck returns a readiness probe: it passes only while the database
// answers and its schema is at least the version this build expects.
// Migrations run in a separate job, so a forgotten migration must keep the
// service out of rotation instead of failing on the first query.
func SchemaCheck(pool *pgxpool.Pool, requiredVersion int64) func(context.Context) error {
	if pool == nil {
		panic("store: pool is required")
	}
	return func(ctx context.Context) error {
		if err := pool.Ping(ctx); err != nil {
			return errors.New("database is unavailable")
		}
		var applied int64
		err := pool.QueryRow(ctx,
			`SELECT COALESCE(MAX(version_id), 0) FROM goose_db_version WHERE is_applied`,
		).Scan(&applied)
		if err != nil {
			return errors.New("schema version is unavailable")
		}
		if applied < requiredVersion {
			return errors.New("database schema is behind this build")
		}
		return nil
	}
}
