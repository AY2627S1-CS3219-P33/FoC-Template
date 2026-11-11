// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-10-17
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

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

	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/app"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/config"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/logging"
)

func main() {
	if err := run(); err != nil {
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).Error(
			"chat service stopped",
			"error", err.Error(),
		)
		os.Exit(1)
	}
}

func run() error {
	// Local convenience: load .env if present. Variables already set in the
	// environment (ECS, Compose, CI) take precedence and are never overridden.
	if err := godotenv.Load(); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return errors.New("could not read .env")
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
		"chat service starting",
		"environment", cfg.Environment,
		"address", cfg.HTTP.Address(),
		"closeDelay", cfg.Chat.CloseDelay.String(),
	)

	return application.Run(ctx)
}
