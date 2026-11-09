// Package app is chat-service's composition root: it builds every component,
// mounts the routes and runs the HTTP server. Background workers (the
// RabbitMQ consumer, and later the Redis broadcaster) are started here too as
// they are added.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/config"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/httpapi"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/readiness"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/store"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/migrations"
)

type Application struct {
	database        *pgxpool.Pool
	server          *http.Server
	shutdownTimeout time.Duration
	logger          *slog.Logger
}

// New composes the service and checks that PostgreSQL answers within the
// startup timeout, so a wrong DATABASE_URL fails at start rather than on the
// first request.
func New(ctx context.Context, cfg config.Config, logger *slog.Logger) (*Application, error) {
	if logger == nil {
		return nil, errors.New("app: logger is required")
	}
	pool, err := store.NewPool(ctx, cfg.Database)
	if err != nil {
		return nil, err
	}
	startupCtx, cancel := context.WithTimeout(ctx, cfg.StartupTimeout)
	defer cancel()
	if err := pool.Ping(startupCtx); err != nil {
		pool.Close()
		// The driver's error may name the host and user, so it is not wrapped.
		return nil, errors.New("could not connect to PostgreSQL")
	}

	schemaVersion, err := migrations.LatestVersion()
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}

	router := httpapi.NewRouter(
		readiness.NewHandler(readiness.Postgres(store.SchemaCheck(pool, schemaVersion))),
	)

	return &Application{
		database: pool,
		server: &http.Server{
			Addr:    cfg.HTTP.Address(),
			Handler: router,
			// No ReadTimeout or WriteTimeout: net/http sets them as deadlines
			// on the connection, and they would stay on it after a WebSocket
			// upgrade and cut long-lived chats. The WebSocket code bounds each
			// frame itself instead.
			ReadHeaderTimeout: 5 * time.Second,
			IdleTimeout:       60 * time.Second,
			ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
		},
		shutdownTimeout: cfg.ShutdownTimeout,
		logger:          logger,
	}, nil
}

// Handler is the full HTTP handler, for tests through httptest.
func (a *Application) Handler() http.Handler {
	return a.server.Handler
}

// Run serves HTTP until ctx is cancelled or the server fails, then stops
// accepting requests and lets in-flight ones finish within the shutdown
// timeout.
func (a *Application) Run(ctx context.Context) error {
	serverResult := make(chan error, 1)
	go func() {
		err := a.server.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serverResult <- err
	}()

	select {
	case err := <-serverResult:
		return fmt.Errorf("serve HTTP: %w", err)
	case <-ctx.Done():
	}

	a.logger.Info("chat service shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), a.shutdownTimeout)
	defer cancel()
	if err := a.server.Shutdown(shutdownCtx); err != nil {
		return errors.New("graceful HTTP shutdown failed")
	}
	if err := <-serverResult; err != nil {
		return fmt.Errorf("serve HTTP: %w", err)
	}
	return nil
}

// Close releases the connections. Call it after Run has returned.
func (a *Application) Close() {
	a.database.Close()
}
