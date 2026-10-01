package supplierrepo

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
)

func TestRepositoryErrorLogIsStructuredAndOmitsInputs(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
	repository := New(nil, logger)
	secretInput := "postgresql://admin:secret@database/private supplier payload"

	_, err := repository.ListAvailable(context.Background(), supplier.ListFilter{
		Query: secretInput,
		Limit: supplier.MaximumPageSize + 1,
	})

	require.Error(t, err)
	require.JSONEq(t, `{
		"time":"TIME",
		"level":"INFO",
		"msg":"supplier repository initialized",
		"component":"supplier_repository"
	}`, normalizeLogTime(t, firstLogLine(output.String())))
	require.Contains(t, output.String(), `"operation":"list_available"`)
	require.Contains(t, output.String(), `"error_kind":"validation"`)
	require.NotContains(t, output.String(), secretInput)
	require.NotContains(t, output.String(), "admin:secret")
}

func firstLogLine(value string) string {
	for index, character := range value {
		if character == '\n' {
			return value[:index]
		}
	}
	return value
}

func normalizeLogTime(t *testing.T, value string) string {
	t.Helper()
	var fields map[string]any
	require.NoError(t, json.Unmarshal([]byte(value), &fields))
	fields["time"] = "TIME"
	normalized, err := json.Marshal(fields)
	require.NoError(t, err)
	return string(normalized)
}
