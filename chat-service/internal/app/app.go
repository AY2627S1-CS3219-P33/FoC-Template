// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-10
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

// Package app is chat-service's composition root: it builds every component,
// mounts the routes, and runs the HTTP server next to the background workers
// (the RabbitMQ errand-event consumer and the broadcaster).
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

	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/broadcast"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/chat"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/config"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/events"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/httpapi"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/hub"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/readiness"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/store"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/ws"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/migrations"
)

// worker is one background loop. run returns when ctx is cancelled.
type worker struct {
	name string
	run  func(ctx context.Context) error
}

type Application struct {
	database        *pgxpool.Pool
	server          *http.Server
	sockets         *ws.Handler
	workers         []worker
	shutdownTimeout time.Duration
	logger          *slog.Logger
}

// New composes the service and checks that PostgreSQL answers within the
// startup timeout, so a wrong DATABASE_URL fails at start rather than on the
// first request. It contacts neither Auth0 nor RabbitMQ: signing keys are
// fetched on the first request that needs them, and the consumer connects
// (and reconnects) on its own once Run starts. authOptions let tests point
// the verifier at a local key server.
func New(ctx context.Context, cfg config.Config, logger *slog.Logger, authOptions ...auth.Auth0Option) (*Application, error) {
	if logger == nil {
		return nil, errors.New("app: logger is required")
	}
	verifier, err := auth.NewAuth0(cfg.Auth, logger, authOptions...)
	if err != nil {
		return nil, err
	}
	guard := auth.NewMiddleware(verifier)

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

	// Live delivery: chat tells the notifier what changed (after commit), the
	// notifier publishes frames through the broadcaster, and the broadcaster
	// hands them to the hub, which holds this copy's open WebSockets. Only
	// the broadcaster changes when several copies run (Redis).
	connections := hub.New(cfg.WebSocket.MaxConnectionsPerUser)
	broadcaster := broadcast.NewLocal(connections)
	notifier := broadcast.NewNotifier(broadcaster, nil, logger)

	rooms := store.New(pool)
	chats := chat.NewService(rooms, chat.Options{
		MaxMessageLength: cfg.Chat.MaxMessageLength,
		Notifier:         notifier,
	})
	sockets := ws.NewHandler(verifier, chats, connections, ws.Config{
		AllowedOrigins: cfg.WebSocket.AllowedOrigins,
		AuthTimeout:    cfg.WebSocket.AuthTimeout,
		PingInterval:   cfg.WebSocket.PingInterval,
		SendRate:       cfg.Chat.SendRate,
		SendBurst:      cfg.Chat.SendBurst,
	}, logger)
	consumer, err := events.NewConsumer(cfg.AMQP.URL,
		events.NewHandler(chat.NewLifecycle(rooms, cfg.Chat.CloseDelay, notifier), logger),
		logger, events.Config{Prefetch: cfg.AMQP.Prefetch})
	if err != nil {
		pool.Close()
		return nil, err
	}

	router := httpapi.NewRouter(
		httpapi.NewChatHandler(chats, guard, logger),
		sockets,
		readiness.NewHandler(
			readiness.Postgres(store.SchemaCheck(pool, schemaVersion)),
			readiness.ErrandEvents(consumer.Running),
		),
	)

	return &Application{
		database: pool,
		sockets:  sockets,
		workers: []worker{
			{name: "errand-events", run: consumer.Run},
			{name: "broadcaster", run: broadcaster.Run},
		},
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

// Run serves HTTP and runs the workers until ctx is cancelled or one of them
// fails. It then stops accepting requests, lets in-flight requests finish,
// and waits for each worker to finish the event it is handling, all within
// the shutdown timeout.
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
				return // stopped on purpose
			}
			if err == nil {
				err = errors.New("stopped unexpectedly")
			}
			failed <- fmt.Errorf("%s: %w", w.name, err)
		}()
	}

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

	a.logger.Info("chat service shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), a.shutdownTimeout)
	defer cancel()
	// WebSockets first: http.Server.Shutdown neither closes nor waits for
	// upgraded connections. 1012 tells each browser to reconnect at once.
	if err := a.sockets.Shutdown(shutdownCtx); err != nil {
		runErr = errors.Join(runErr, err)
	}
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
	a.database.Close()
}
