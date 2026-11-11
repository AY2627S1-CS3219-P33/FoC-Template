// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: Implementation of notification-service structured service logging.
// Author review: I validated correctness, edited for style, and added boundary checks.

// Package logging builds the service's structured JSON logger.
package logging

import (
	"io"
	"log/slog"
)

func New(output io.Writer, configuredLevel string) *slog.Logger {
	level := slog.LevelInfo
	switch configuredLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}

	return slog.New(slog.NewJSONHandler(output, &slog.HandlerOptions{Level: level}))
}
