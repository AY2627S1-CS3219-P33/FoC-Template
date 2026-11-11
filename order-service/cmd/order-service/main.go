// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-10-22
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/app"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/config"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/logging"
)

func main() {
	if err := run(); err != nil {
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).Error(
			"order service stopped",
			"error", err.Error(),
		)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := logging.New(os.Stdout, cfg.LogLevel)
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	application, err := app.New(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer application.Close()

	logger.Info(
		"order service starting",
		"environment", cfg.Environment,
		"address", cfg.HTTP.Address(),
	)

	return application.Run(ctx)
}
