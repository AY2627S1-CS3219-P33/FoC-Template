package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	_ "time/tzdata" // the Asia/Singapore day boundaries must not depend on the image's zoneinfo

	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/app"
	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/config"
	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/logging"
)

func main() {
	if err := run(); err != nil {
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).Error(
			"admin service stopped",
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
		"admin service starting",
		"environment", cfg.Environment,
		"address", cfg.HTTP.Address(),
	)

	return application.Run(ctx)
}
