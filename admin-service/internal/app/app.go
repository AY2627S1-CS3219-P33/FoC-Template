// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-09
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

// Package app is admin-service's composition root. It builds the HTTP server
// and runs it next to the background workers (the event consumer).
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/config"
	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/dashboardapi"
	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/database"
	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/enrichment"
	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/events"
	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/facts"
	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/httpapi"
	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/metrics"
	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/orderclient"
	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/readiness"
	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/serviceauth"
	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/servicehealth"
)

// worker is one background loop. run returns when ctx is cancelled.
type worker struct {
	name string
	run  func(ctx context.Context) error
}

type Application struct {
	database        *pgxpool.Pool
	orderTokens     serviceauth.TokenSource
	server          *http.Server
	workers         []worker
	shutdownTimeout time.Duration
	logger          *slog.Logger
}

// Option adjusts how New composes the service; production passes none.
type Option func(*options)

type options struct {
	authOptions []auth.Auth0Option
	httpClient  *http.Client
	enrichment  enrichment.Config
}

// WithTrustedClient makes the Auth0 key fetch and the machine-to-machine
// token request use client, for a deployment-controlled trust store or the
// fake tenant in tests.
func WithTrustedClient(client *http.Client) Option {
	return func(o *options) {
		o.httpClient = client
		o.authOptions = append(o.authOptions, auth.WithHTTPClient(client))
	}
}

// WithEnrichment overrides the enrichment worker's timing.
func WithEnrichment(cfg enrichment.Config) Option {
	return func(o *options) { o.enrichment = cfg }
}

// New composes the service. It checks that PostgreSQL answers but does not
// contact RabbitMQ: the consumer connects on its own and /readyz reports it.
func New(ctx context.Context, cfg config.Config, logger *slog.Logger, opts ...Option) (*Application, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	verifier, err := auth.NewAuth0(cfg.Auth, logger, o.authOptions...)
	if err != nil {
		return nil, err
	}
	guard := auth.NewMiddleware(verifier)

	tokens, err := serviceauth.NewClientCredentials(cfg.Order.Credentials, o.httpClient)
	if err != nil {
		return nil, err
	}
	orders, err := orderclient.New(cfg.Order.BaseURL, tokens, cfg.Order.Timeout)
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

	application := &Application{
		database:        pool,
		orderTokens:     tokens,
		shutdownTimeout: cfg.ShutdownTimeout,
		logger:          logger,
	}

	store := facts.NewStore(pool)
	enricher, err := enrichment.NewWorker(store, orders, logger, o.enrichment)
	if err != nil {
		pool.Close()
		return nil, err
	}
	consumer, err := events.NewConsumer(cfg.AMQP.URL, events.NewHandler(store, enricher.Wake, logger), logger,
		events.Config{Prefetch: cfg.AMQP.Prefetch})
	if err != nil {
		pool.Close()
		return nil, err
	}
	application.workers = []worker{
		{name: "event-consumer", run: consumer.Run},
		{name: "enrichment", run: enricher.Run},
	}

	ready := readiness.NewHandler(
		readiness.Postgres(pool.Ping),
		readiness.Events(consumer.Running),
	)
	services, err := servicehealth.New(readinessTargets(cfg), notChecked(cfg), cfg.Readiness.Timeout)
	if err != nil {
		pool.Close()
		return nil, err
	}
	router := httpapi.NewRouter(httpapi.Dependencies{Logger: logger},
		dashboardapi.NewMetricsHandler(metrics.NewService(pool, cfg.Timezone), time.Now, guard, logger),
		dashboardapi.NewReadinessHandler(services, time.Now, guard, logger),
		ready)

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

// readinessTargets are the services the dashboard reads /readyz from, in the
// order it shows them. admin-service reads its own through the loopback, so
// its event consumer's state shows beside the others'.
func readinessTargets(cfg config.Config) []servicehealth.Target {
	targets := []servicehealth.Target{{Name: "order-service", URL: cfg.Order.BaseURL}}
	if cfg.Readiness.SupplierURL != "" {
		targets = append(targets, servicehealth.Target{Name: "supplier-service", URL: cfg.Readiness.SupplierURL})
	}
	if cfg.Readiness.CreditURL != "" {
		targets = append(targets, servicehealth.Target{Name: "credit-service", URL: cfg.Readiness.CreditURL})
	}
	if cfg.Readiness.UserURL != "" {
		targets = append(targets, servicehealth.Target{Name: "user-service", URL: cfg.Readiness.UserURL})
	}
	return append(targets, servicehealth.Target{
		Name: "admin-service", URL: "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.HTTP.Port)),
	})
}

// notChecked are the services the dashboard shows but is not set up to check:
// those whose URL is not configured, so a missing service is listed with its
// reason instead of silently absent.
func notChecked(cfg config.Config) []servicehealth.NotChecked {
	var out []servicehealth.NotChecked
	for _, service := range []struct{ name, key, url string }{
		{"supplier-service", "SUPPLIER_SERVICE_URL", cfg.Readiness.SupplierURL},
		{"credit-service", "CREDIT_SERVICE_URL", cfg.Readiness.CreditURL},
		{"user-service", "USER_SERVICE_URL", cfg.Readiness.UserURL},
	} {
		if service.url == "" {
			out = append(out, servicehealth.NotChecked{Name: service.name, Reason: service.key + " is not configured"})
		}
	}
	return out
}

func (a *Application) Handler() http.Handler {
	return a.server.Handler
}

// Run serves HTTP and runs the workers until ctx is cancelled or one of
// them fails. It then stops accepting requests, lets in-flight requests
// finish, and waits for every worker to finish its current delivery, all
// within the shutdown timeout.
func (a *Application) Run(ctx context.Context) error {
	workerCtx, stopWorkers := context.WithCancel(ctx)
	defer stopWorkers()

	failed := make(chan error, len(a.workers)+1)
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
	a.logger.Info("background workers started", "workers", names)

	// Fetch the order-service token now. Otherwise the first enrichment waits
	// for Auth0 inside its request timeout.
	go func() {
		if _, err := a.orderTokens.Token(workerCtx); err != nil && workerCtx.Err() == nil {
			a.logger.Warn("could not fetch the order-service token; retrying on the first call", "error", err.Error())
		}
	}()

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

// Close releases the database connections. Call it after Run has returned.
func (a *Application) Close() {
	a.database.Close()
}
