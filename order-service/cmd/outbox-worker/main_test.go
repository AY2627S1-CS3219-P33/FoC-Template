package main

import (
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunRejectsInvalidConfiguration(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	t.Setenv("DATABASE_URL", "")
	t.Setenv("AMQP_URL", "")
	require.EqualError(t, run(logger), "DATABASE_URL and AMQP_URL are required")
	t.Setenv("DATABASE_URL", "postgres://localhost/order")
	t.Setenv("AMQP_URL", "amqp://localhost/")
	t.Setenv("AMQP_DECLARE_TAP", "not-a-boolean")
	require.EqualError(t, run(logger), "AMQP_DECLARE_TAP must be a boolean")
	t.Setenv("AMQP_DECLARE_TAP", "false")
	t.Setenv("AMQP_URL", "http://user:secret@localhost/")
	err := run(logger)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "secret")
}
