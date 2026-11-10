// Command backfill loads order-service's event history into admin-service's
// read model. Run it once after the service has started (so its queue is
// bound), as a one-off task with the service's own environment. Rerunning it
// is safe: it changes nothing the second time.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/backfill"
	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/config"
	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/database"
	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/facts"
	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/logging"
	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/orderclient"
	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/serviceauth"
)

func main() {
	if err := run(); err != nil {
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).Error("backfill failed", "error", err.Error())
		os.Exit(1)
	}
}

func run() error {
	pageSize := flag.Int("page-size", orderclient.MaxPageSize, "events per request, 1 to 500")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := logging.New(os.Stdout, cfg.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	tokens, err := serviceauth.NewClientCredentials(cfg.Order.Credentials, nil)
	if err != nil {
		return err
	}
	orders, err := orderclient.New(cfg.Order.BaseURL, tokens, cfg.Order.Timeout)
	if err != nil {
		return err
	}
	pool, err := database.NewPool(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer pool.Close()
	startup, cancel := context.WithTimeout(ctx, cfg.StartupTimeout)
	defer cancel()
	if err := pool.Ping(startup); err != nil {
		return errors.New("could not connect to PostgreSQL")
	}

	result, err := backfill.Run(ctx, orders, facts.NewStore(pool), logger, backfill.Config{PageSize: *pageSize})
	logger.Info("backfill finished",
		"pages", result.Pages, "events", result.Events, "applied", result.Applied, "rejected", result.Rejected)
	return err
}
