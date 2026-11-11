// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-09-29
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

package database

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/config"
)

func NewPool(ctx context.Context, cfg config.DatabaseConfig) (*pgxpool.Pool, error) {
	poolConfig, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		// Do not wrap the parser error because it may contain the connection URL.
		return nil, errors.New("invalid PostgreSQL connection configuration")
	}

	poolConfig.MinConns = cfg.MinConnections
	poolConfig.MaxConns = cfg.MaxConnections

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, errors.New("could not initialize PostgreSQL connection pool")
	}

	return pool, nil
}
