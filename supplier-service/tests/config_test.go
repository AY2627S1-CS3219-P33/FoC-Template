package tests

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/config"
)

func TestConfigurationUsesIsolatedTestDatabase(t *testing.T) {
	testDatabaseURL := "postgresql://supplier_test@127.0.0.1:5433/supplier_test"
	values := map[string]string{
		"APP_ENV":           "test",
		"TEST_DATABASE_URL": testDatabaseURL,
	}

	cfg, err := config.LoadFrom(mapLookup(values))

	require.NoError(t, err)
	require.Equal(t, config.Test, cfg.Environment)
	require.Equal(t, testDatabaseURL, cfg.Database.URL)
	require.Equal(t, 3002, cfg.HTTP.Port)
}

func TestConfigurationRejectsMissingTestDatabaseWithoutLeakingValues(t *testing.T) {
	values := map[string]string{
		"APP_ENV":      "test",
		"DATABASE_URL": "postgresql://secret-user:secret-password@database/supplier",
	}

	_, err := config.LoadFrom(mapLookup(values))

	require.Error(t, err)
	require.Contains(t, err.Error(), "TEST_DATABASE_URL")
	require.NotContains(t, err.Error(), "secret-password")
}

func mapLookup(values map[string]string) config.LookupEnv {
	return func(key string) (string, bool) {
		value, found := values[key]
		return value, found
	}
}
