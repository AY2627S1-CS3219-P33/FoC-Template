// Package app assembles the HTTP handler and the server lifecycle.
package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/config"
	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/database"
	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/readiness"
	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/wallet"
	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/walletapi"
	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/migrations"
)

// Deps are the collaborators the HTTP routes need.
type Deps struct {
	Readiness readiness.Checker
	Auth      auth.Port
	Wallets   walletapi.Service
	Logger    *slog.Logger
}

// Handler builds the HTTP routes. It opens no network port, so tests can
// call it directly with httptest.
func Handler(deps Deps) http.Handler {
	router := http.NewServeMux()
	readiness.NewHandler(deps.Readiness).RegisterRoutes(router)
	walletapi.NewHandler(deps.Wallets, auth.NewMiddleware(deps.Auth), deps.Logger).RegisterRoutes(router)
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
	verifier, err := auth.NewAuth0(cfg.Auth, logger)
	if err != nil {
		pool.Close()
		return nil, err
	}

	return &App{
		cfg:    cfg,
		logger: logger,
		pool:   pool,
		server: &http.Server{
			Addr: cfg.HTTP.Address(),
			Handler: Handler(Deps{
				Readiness: readiness.NewPostgresChecker(pool, requiredVersion),
				Auth:      verifier,
				Wallets:   wallet.NewService(pool, cfg.InitialCredit),
				Logger:    logger,
			}),
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
