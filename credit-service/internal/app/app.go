// Package app assembles the HTTP handler and the server lifecycle.
package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/config"
	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/database"
	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/readiness"
	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/migrations"
)

// Handler builds the HTTP routes. It opens no network port, so tests can
// call it directly with httptest.
func Handler(checker readiness.Checker) http.Handler {
	router := http.NewServeMux()
	readiness.NewHandler(checker).RegisterRoutes(router)
	return router
}

type App struct {
	cfg    config.Config
	logger *slog.Logger
	pool   *pgxpool.Pool
	server *http.Server
}

// New connects to PostgreSQL and builds the server. It does not migrate the
// schema; the runtime role has no DDL rights (see infra/migrate.sh).
func New(ctx context.Context, cfg config.Config, logger *slog.Logger) (*App, error) {
	startupCtx, cancel := context.WithTimeout(ctx, cfg.StartupTimeout)
	defer cancel()

	pool, err := database.NewPool(startupCtx, cfg.Database)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(startupCtx); err != nil {
		pool.Close()
		return nil, errors.New("database is unavailable")
	}
	requiredVersion, err := migrations.LatestVersion()
	if err != nil {
		pool.Close()
		return nil, err
	}

	return &App{
		cfg:    cfg,
		logger: logger,
		pool:   pool,
		server: &http.Server{
			Addr:              cfg.HTTP.Address(),
			Handler:           Handler(readiness.NewPostgresChecker(pool, requiredVersion)),
			ReadHeaderTimeout: 5 * time.Second,
		},
	}, nil
}

// Run serves until ctx is cancelled, then shuts down gracefully.
func (a *App) Run(ctx context.Context) error {
	errs := make(chan error, 1)
	go func() { errs <- a.server.ListenAndServe() }()

	select {
	case err := <-errs:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), a.cfg.ShutdownTimeout)
	defer cancel()
	a.logger.Info("credit service shutting down")
	return a.server.Shutdown(shutdownCtx)
}

func (a *App) Close() { a.pool.Close() }
