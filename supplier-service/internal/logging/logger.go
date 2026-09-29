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
