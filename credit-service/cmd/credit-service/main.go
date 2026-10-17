package main

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/joho/godotenv"

	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/app"
	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/config"
	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/logging"
)

func main() {
	if err := run(); err != nil {
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).Error(
			"credit service stopped",
			"error", err.Error(),
		)
		os.Exit(1)
	}
}

func run() error {
	// Local convenience: load .env if present. Variables already set in the
	// environment (ECS, compose, CI) take precedence and are never overridden.
	if err := godotenv.Load(); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := logging.New(os.Stdout, cfg.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	application, err := app.New(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer application.Close()

	logger.Info(
		"credit service starting",
		"environment", cfg.Environment,
		"address", cfg.HTTP.Address(),
	)

	return application.Run(ctx)
}
