// Package app assembles the HTTP handler and the server lifecycle.
package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/config"
	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/creditmsg"
	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/database"
	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/database/outboxrepo"
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
	cfg        config.Config
	logger     *slog.Logger
	pool       *pgxpool.Pool
	server     *http.Server
	consumer   *creditmsg.Consumer
	dispatcher *creditmsg.Dispatcher
	publisher  *creditmsg.Publisher
}

// New connects to PostgreSQL and builds the server, the consumer of order
// commands and the dispatcher of results. It does not migrate the schema (the
// runtime role has no DDL rights, see infra/migrate.sh) and it does not
// connect to RabbitMQ: the consumer and the dispatcher do that in Run, and
// retry while the broker is away.
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

	// Every order command stores its result in the outbox, in its own
	// transaction; the dispatcher publishes it.
	service := wallet.NewService(pool, cfg.InitialCredit).WithResultRecorder(creditmsg.NewOutboxRecorder())
	consumer, err := creditmsg.NewConsumer(cfg.Messaging.URL, creditmsg.NewHandler(service), logger, creditmsg.Config{
		Prefetch: cfg.Messaging.ConsumerPrefetch,
	})
	if err != nil {
		pool.Close()
		return nil, err
	}
	publisher, err := creditmsg.NewPublisher(cfg.Messaging.URL)
	if err != nil {
		pool.Close()
		return nil, err
	}
	dispatcher, err := creditmsg.NewDispatcher(outboxrepo.Store{DB: pool}, publisher, logger, creditmsg.DispatcherConfig{
		PollInterval: cfg.Messaging.OutboxPollInterval,
		BatchSize:    cfg.Messaging.OutboxBatchSize,
		MaxAttempts:  cfg.Messaging.OutboxMaxAttempts,
	})
	if err != nil {
		_ = publisher.Close()
		pool.Close()
		return nil, err
	}

	return &App{
		cfg:        cfg,
		logger:     logger,
		pool:       pool,
		consumer:   consumer,
		dispatcher: dispatcher,
		publisher:  publisher,
		server: &http.Server{
			Addr: cfg.HTTP.Address(),
			Handler: Handler(Deps{
				// Not ready until the database is current and the consumer is
				// subscribed, so no traffic is routed to an instance that cannot
				// process commands.
				Readiness: readiness.All(
					readiness.NewPostgresChecker(pool, requiredVersion),
					readiness.CheckerFunc(func(context.Context) error {
						if !consumer.Running() {
							return errors.New("command consumer is not running")
						}
						return nil
					}),
				),
				Auth:    verifier,
				Wallets: service,
				Logger:  logger,
			}),
			ReadHeaderTimeout: 5 * time.Second,
		},
	}, nil
}

// Run serves HTTP, consumes commands and publishes results until ctx is
// cancelled or one of them fails, then stops all three. The HTTP server shuts
// down gracefully, and the consumer and the dispatcher finish the item they are
// working on. Waiting is bounded by the shutdown timeout.
func (a *App) Run(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	const parts = 3
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
				a.logger.Error("credit service part stopped", "part", name, "error", err.Error())
			}
			errs <- err
		}()
	}
	start("http", a.server.ListenAndServe)
	start("command consumer", func() error { return a.consumer.Run(runCtx) })
	start("result dispatcher", func() error { return a.dispatcher.Run(runCtx) })

	var first error
	select {
	case first = <-errs:
	case <-ctx.Done():
	}

	a.logger.Info("credit service shutting down")
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
		a.logger.Warn("credit service parts did not stop in time")
	}
	return first
}

func (a *App) Close() {
	_ = a.publisher.Close()
	a.pool.Close()
}
