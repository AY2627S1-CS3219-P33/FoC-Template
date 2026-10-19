// outbox-worker publishes existing outbox rows. It does not serve errand HTTP
// routes or consume credit results; those require the production errand Store.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/msgrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/database/opsrepo"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/messaging"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("outbox worker stopped", slog.Any("error", err))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	databaseURL, amqpURL := os.Getenv("DATABASE_URL"), os.Getenv("AMQP_URL")
	if databaseURL == "" || amqpURL == "" {
		return errors.New("DATABASE_URL and AMQP_URL are required")
	}
	tap := false
	if value := os.Getenv("AMQP_DECLARE_TAP"); value != "" {
		var err error
		tap, err = strconv.ParseBool(value)
		if err != nil {
			return errors.New("AMQP_DECLARE_TAP must be a boolean")
		}
	}
	publisher, err := messaging.NewPublisher(amqpURL, tap)
	if err != nil {
		return err
	}
	defer publisher.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return errors.New("invalid database configuration")
	}
	defer pool.Close()
	startupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err = pool.Ping(startupCtx)
	cancel()
	if err != nil {
		return errors.New("database connection failed")
	}
	worker := rand.Text()
	logger.Info("outbox worker started", slog.String("worker", worker), slog.Bool("event_tap", tap))
	dispatcher := messaging.NewDispatcher(msgrepo.New(pool), publisher, opsrepo.New(pool), logger, worker)
	err = dispatcher.Run(ctx)
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		return nil
	}
	return err
}
