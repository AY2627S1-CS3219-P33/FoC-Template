// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: Implementation of notification-service PostgreSQL connection management.
// Author review: I validated correctness, edited for style, and added boundary checks.

// Package database creates the PostgreSQL connection pool.
package database

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CS3219-AY2627S1/FoC-Template/notification-service/internal/config"
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
