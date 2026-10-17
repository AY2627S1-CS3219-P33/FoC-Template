package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func lookupFrom(values map[string]string) LookupEnv {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func TestLoadAppliesDefaults(t *testing.T) {
	cfg, err := LoadFrom(lookupFrom(map[string]string{
		"DATABASE_URL": "postgres://u:p@localhost:5432/credit",
	}))
	require.NoError(t, err)
	require.Equal(t, "0.0.0.0:3004", cfg.HTTP.Address())
	require.Equal(t, Development, cfg.Environment)
	require.Equal(t, int32(10), cfg.Database.MaxConnections)
}

func TestLoadRequiresDatabaseURL(t *testing.T) {
	_, err := LoadFrom(lookupFrom(nil))
	require.ErrorContains(t, err, "DATABASE_URL is required")
}

func TestLoadUsesTestDatabaseURLInTestEnvironment(t *testing.T) {
	cfg, err := LoadFrom(lookupFrom(map[string]string{
		"APP_ENV":           "test",
		"TEST_DATABASE_URL": "postgres://u:p@localhost:5434/credit_test",
	}))
	require.NoError(t, err)
	require.Equal(t, "postgres://u:p@localhost:5434/credit_test", cfg.Database.URL)
}

func TestLoadRejectsInvalidValuesWithoutLeakingThem(t *testing.T) {
	_, err := LoadFrom(lookupFrom(map[string]string{
		"DATABASE_URL":      "mysql://secret-user:secret-pass@host/db",
		"HTTP_PORT":         "99999",
		"DATABASE_POOL_MIN": "5",
		"DATABASE_POOL_MAX": "2",
	}))
	require.Error(t, err)
	require.ErrorContains(t, err, "DATABASE_URL must be a PostgreSQL URL")
	require.ErrorContains(t, err, "HTTP_PORT")
	require.ErrorContains(t, err, "DATABASE_POOL_MIN must not exceed")
	require.NotContains(t, err.Error(), "secret-pass")
}
