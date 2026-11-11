// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: Implementation of notification-service application startup and dependency wiring.
// Author review: I validated correctness, edited for style, and added boundary checks.

// Package app assembles the HTTP handler, the errand-event consumer and the
// server lifecycle.
package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CS3219-AY2627S1/FoC-Template/notification-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/notification-service/internal/config"
	"github.com/CS3219-AY2627S1/FoC-Template/notification-service/internal/database"
	"github.com/CS3219-AY2627S1/FoC-Template/notification-service/internal/database/notifrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/notification-service/internal/notification"
	"github.com/CS3219-AY2627S1/FoC-Template/notification-service/internal/notificationapi"
	"github.com/CS3219-AY2627S1/FoC-Template/notification-service/internal/notifmsg"
	"github.com/CS3219-AY2627S1/FoC-Template/notification-service/internal/notifstream"
	"github.com/CS3219-AY2627S1/FoC-Template/notification-service/internal/readiness"
	"github.com/CS3219-AY2627S1/FoC-Template/notification-service/migrations"
)

// Deps are the collaborators the HTTP routes need.
type Deps struct {
	Readiness     readiness.Checker
	Auth          auth.Port
	Notifications notificationapi.Service
	Stream        notificationapi.Streamer
	Logger        *slog.Logger
}

// Handler builds the HTTP routes. It opens no network port, so tests can call
// it directly with httptest.
func Handler(deps Deps) http.Handler {
	router := http.NewServeMux()
	readiness.NewHandler(deps.Readiness).RegisterRoutes(router)
	notificationapi.NewHandler(deps.Notifications, deps.Stream, auth.NewMiddleware(deps.Auth), deps.Logger).RegisterRoutes(router)
	return router
}

type App struct {
	cfg      config.Config
	logger   *slog.Logger
	pool     *pgxpool.Pool
	server   *http.Server
	consumer *notifmsg.Consumer
}

// New connects to PostgreSQL and builds the server and the consumer of errand
// events. It does not migrate the schema (the runtime role has no DDL rights)
// and it does not connect to RabbitMQ: the consumer does that in Run, and
// retries while the broker is away.
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

	service := notification.NewService(notifrepo.New(pool))
	// The hub is the push side: the consumer records a notification through the
	// recorder, which then wakes the recipient's open HTTP streams.
	hub := notifstream.NewHub()
	recorder := notifstream.NewRecorder(service, hub)
	consumer, err := notifmsg.NewConsumer(cfg.Messaging.URL, notifmsg.NewHandler(recorder), logger, notifmsg.Config{
		Prefetch: cfg.Messaging.ConsumerPrefetch,
	})
	if err != nil {
		pool.Close()
		return nil, err
	}

	return &App{
		cfg:      cfg,
		logger:   logger,
		pool:     pool,
		consumer: consumer,
		server: &http.Server{
			Addr: cfg.HTTP.Address(),
			Handler: Handler(Deps{
				// Not ready until the schema is current and the consumer is
				// subscribed, so no traffic reaches an instance that cannot
				// record events.
				Readiness: readiness.All(
					readiness.NewPostgresChecker(pool, requiredVersion),
					readiness.CheckerFunc(func(context.Context) error {
						if !consumer.Running() {
							return errors.New("event consumer is not running")
						}
						return nil
					}),
				),
				Auth:          verifier,
				Notifications: service,
				Stream:        hub,
				Logger:        logger,
			}),
			ReadHeaderTimeout: 5 * time.Second,
		},
	}, nil
}

// Run serves HTTP and consumes errand events until ctx is cancelled or one of
// them fails, then stops both. The HTTP server shuts down gracefully and the
// consumer finishes the event it is handling. Waiting is bounded by the
// shutdown timeout.
func (a *App) Run(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	const parts = 2
	errs := make(chan error, parts)
	var wg sync.WaitGroup
	start := func(name string, run func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := run()
			if errors.Is(err, http.ErrServerClosed) || errors.Is(err, context.Canceled) {
				err = nil
			}
			if err != nil {
				a.logger.Error("notification service part stopped", "part", name, "error", err.Error())
			}
			errs <- err
		}()
	}
	start("http", a.server.ListenAndServe)
	start("event consumer", func() error { return a.consumer.Run(runCtx) })

	var first error
	select {
	case first = <-errs:
	case <-ctx.Done():
	}

	a.logger.Info("notification service shutting down")
	cancel()
	shutdownCtx, stop := context.WithTimeout(context.Background(), a.cfg.ShutdownTimeout)
	defer stop()
	if err := a.server.Shutdown(shutdownCtx); err != nil && first == nil {
		first = err
	}
	finished := make(chan struct{})
	go func() { wg.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-shutdownCtx.Done():
		a.logger.Warn("notification service parts did not stop in time")
	}
	return first
}

func (a *App) Close() {
	a.pool.Close()
}
