// Package app is order-service's composition root (I1). It selects the
// adapters, mounts every feature's routes on one router, and runs the HTTP
// server next to the background workers: the outbox dispatcher, the
// credit-result consumer and the credit reconcilers (Person C), and the
// deadline and reminder scheduler (Person B).
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/cache"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/config"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/creditmsg"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/errandrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/msgrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/opsrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand/accept"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand/browse"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand/courier"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand/create"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand/mine"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand/requester"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/fence"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/httpapi"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/internalapi"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/messaging"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/outboxadmin"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/readiness"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/reconciler"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/review"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/scheduler"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/serviceauth"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/supplierclient"
)

// worker is one background loop. run returns when ctx is cancelled.
type worker struct {
	name string
	run  func(ctx context.Context) error
}

type Application struct {
	database        *pgxpool.Pool
	redis           *cache.Client // nil when REDIS_URL is empty
	publisher       *messaging.Publisher
	server          *http.Server
	workers         []worker
	shutdownTimeout time.Duration
	logger          *slog.Logger
}

// New composes the service. It checks that PostgreSQL answers but contacts
// neither Redis nor RabbitMQ: both connect lazily and reconnect on their own,
// and /readyz reports them. Auth options allow a deployment-controlled trust
// store, including the JWKS fixture in tests.
func New(ctx context.Context, cfg config.Config, logger *slog.Logger, authOptions ...auth.Auth0Option) (*Application, error) {
	verifier, err := auth.NewAuth0(cfg.Auth, logger, authOptions...)
	if err != nil {
		return nil, err
	}
	guard := auth.NewMiddleware(verifier)
	serviceGuard := serviceauth.NewMiddleware(guard)

	tokens, err := serviceauth.NewClientCredentials(cfg.Supplier.Credentials, nil)
	if err != nil {
		return nil, err
	}
	supplierClient, err := supplierclient.New(supplierclient.Config{
		BaseURL: cfg.Supplier.BaseURL,
		Tokens:  tokens,
		Timeout: cfg.Supplier.Timeout,
	})
	if err != nil {
		return nil, err
	}
	publisher, err := messaging.NewPublisher(cfg.AMQP.URL, cfg.AMQP.DeclareTap)
	if err != nil {
		return nil, err
	}

	pool, err := database.NewPool(ctx, cfg.Database)
	if err != nil {
		return nil, err
	}
	startupCtx, cancel := context.WithTimeout(ctx, cfg.StartupTimeout)
	defer cancel()
	if err := pool.Ping(startupCtx); err != nil {
		pool.Close()
		return nil, errors.New("could not connect to PostgreSQL")
	}

	// An empty REDIS_URL selects the no-op caches; every read then goes to
	// PostgreSQL or supplier-service.
	var (
		redis      *cache.Client
		kv         cache.KV               = cache.NoopKV{}
		openCache  errand.OpenErrandCache = cache.NoopOpenErrandCache{}
		redisCheck func(context.Context) error
	)
	if cfg.RedisURL != "" {
		redis, err = cache.Open(cfg.RedisURL, cache.Config{Logger: logger})
		if err != nil {
			pool.Close()
			return nil, err
		}
		kv, openCache, redisCheck = redis, cache.NewRedisOpenErrandCache(redis), redis.Ping
	}

	clock := errand.SystemClock{}
	store := errandrepo.New(pool)
	ops := opsrepo.New(pool)
	outbox := msgrepo.New(pool)
	suppliers := supplierclient.NewCachedGate(supplierClient, kv)

	application := &Application{
		database:        pool,
		redis:           redis,
		publisher:       publisher,
		shutdownTimeout: cfg.ShutdownTimeout,
		logger:          logger,
	}
	fail := func(err error) (*Application, error) {
		application.Close()
		return nil, err
	}

	dispatcher, err := messaging.NewDispatcher(outbox, publisher, ops, logger, messaging.DispatcherConfig{})
	if err != nil {
		return fail(err)
	}
	creditResults, err := creditmsg.NewHandler(store, outbox, clock, logger, nil)
	if err != nil {
		return fail(err)
	}
	consumer, err := creditmsg.NewConsumer(cfg.AMQP.URL, creditResults, logger, creditmsg.Config{Prefetch: cfg.AMQP.Prefetch})
	if err != nil {
		return fail(err)
	}
	reconcilers, err := reconciler.New(store, outbox, ops, clock, reconciler.Config{Logger: logger})
	if err != nil {
		return fail(err)
	}
	deadlines := scheduler.New(store, clock, scheduler.Config{Reminders: ops, Settings: ops, Logger: logger})
	application.workers = []worker{
		{name: "outbox-dispatcher", run: dispatcher.Run},
		{name: "credit-results", run: consumer.Run},
		{name: "reconciler", run: func(ctx context.Context) error { reconcilers.Run(ctx); return nil }},
		{name: "scheduler", run: func(ctx context.Context) error { deadlines.Run(ctx); return nil }},
	}

	reviews := review.Services{
		Reviews: review.NewService(review.Dependencies{
			Store: store, Reviews: ops, Audit: ops, Suppliers: suppliers, Clock: clock, Logger: logger,
		}),
		Settings: review.NewSettingsService(review.NewRepositorySettings(ops), ops, logger),
		Events:   review.NewEventService(ops),
		Flags:    review.NewFlagService(ops, ops, clock),
	}
	ready := readiness.NewHandler(
		readiness.Postgres(pool.Ping),
		readiness.Redis(redisCheck),
		readiness.RabbitMQ(publisher.Ping),
		readiness.CreditResults(consumer.Running),
		readiness.Breaker(readiness.NameSupplier, supplierClient.Breaker()),
	)

	router := httpapi.NewRouter(httpapi.Dependencies{Logger: logger},
		create.NewHandler(create.Dependencies{Store: store, Suppliers: suppliers, Clock: clock, Logger: logger}, guard),
		browse.NewHandler(browse.Dependencies{Store: store, Suppliers: suppliers, Cache: openCache, Clock: clock, Logger: logger}, guard),
		accept.NewHandler(accept.Dependencies{Store: store, Suppliers: suppliers, Settings: ops, Clock: clock, Logger: logger}, guard),
		requester.NewHandler(requester.Dependencies{Store: store, Suppliers: suppliers, Clock: clock, Logger: logger}, guard),
		courier.NewHandler(courier.Dependencies{Store: store, Suppliers: suppliers, Settings: ops, Clock: clock, Logger: logger}, guard),
		mine.NewHandler(mine.Dependencies{Store: store, Suppliers: suppliers, Logger: logger}, guard),
		review.NewHandler(reviews, guard, logger),
		outboxadmin.NewHandler(outboxadmin.NewStore(pool), guard, logger),
		fence.NewHandler(fence.NewService(ops, logger), serviceGuard, logger),
		internalapi.NewHandler(ops, serviceGuard, logger),
		ready,
	)

	application.server = &http.Server{
		Addr:              cfg.HTTP.Address(),
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	return application, nil
}

func (a *Application) Handler() http.Handler {
	return a.server.Handler
}

// Run serves HTTP and runs the workers until ctx is cancelled or one of
// them fails. It then stops accepting requests, lets in-flight requests
// finish, and waits for every worker to finish its current batch or
// delivery, all within the shutdown timeout.
func (a *Application) Run(ctx context.Context) error {
	workerCtx, stopWorkers := context.WithCancel(ctx)
	defer stopWorkers()

	failed := make(chan error, len(a.workers))
	var workers sync.WaitGroup
	for _, w := range a.workers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			err := w.run(workerCtx)
			if workerCtx.Err() != nil {
				return
			}
			if err == nil {
				err = errors.New("stopped unexpectedly")
			}
			failed <- fmt.Errorf("%s: %w", w.name, err)
		}()
	}
	names := make([]string, len(a.workers))
	for i, w := range a.workers {
		names[i] = w.name
	}
	a.logger.Info("background workers started", "workers", names, "redis", a.redis != nil)

	serverResult := make(chan error, 1)
	go func() {
		err := a.server.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serverResult <- err
	}()

	var runErr error
	serving := true
	select {
	case err := <-serverResult:
		serving = false
		runErr = fmt.Errorf("serve HTTP: %w", err)
	case err := <-failed:
		runErr = err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), a.shutdownTimeout)
	defer cancel()
	if serving {
		if err := a.server.Shutdown(shutdownCtx); err != nil {
			runErr = errors.Join(runErr, errors.New("graceful HTTP shutdown failed"))
		} else if err := <-serverResult; err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("serve HTTP: %w", err))
		}
	}

	stopWorkers()
	stopped := make(chan struct{})
	go func() {
		workers.Wait()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-shutdownCtx.Done():
		runErr = errors.Join(runErr, errors.New("background workers did not stop in time"))
	}
	return runErr
}

// Close releases the connections. Call it after Run has returned.
func (a *Application) Close() {
	_ = a.publisher.Close()
	if a.redis != nil {
		_ = a.redis.Close()
	}
	a.database.Close()
}
