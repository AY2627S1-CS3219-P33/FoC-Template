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

func validEnv(extra map[string]string) map[string]string {
	env := map[string]string{
		"DATABASE_URL":          "postgres://u:p@localhost:5432/credit",
		"INITIAL_CREDIT_AMOUNT": "100",
		"AUTH0_ISSUER":          "https://example.auth0.com/",
	}
	for key, value := range extra {
		env[key] = value
	}
	return env
}

func TestLoadAppliesDefaults(t *testing.T) {
	cfg, err := LoadFrom(lookupFrom(validEnv(nil)))
	require.NoError(t, err)
	require.Equal(t, int64(100), cfg.InitialCredit)
	require.Equal(t, "https://api.foc.local/credit-service", cfg.Auth.Audience)
	require.Equal(t, "0.0.0.0:3004", cfg.HTTP.Address())
	require.Equal(t, Development, cfg.Environment)
	require.Equal(t, int32(10), cfg.Database.MaxConnections)
}

func TestLoadRequiresDatabaseURL(t *testing.T) {
	_, err := LoadFrom(lookupFrom(validEnv(map[string]string{"DATABASE_URL": ""})))
	require.ErrorContains(t, err, "DATABASE_URL is required")
}

func TestLoadUsesTestDatabaseURLInTestEnvironment(t *testing.T) {
	cfg, err := LoadFrom(lookupFrom(validEnv(map[string]string{
		"APP_ENV":           "test",
		"DATABASE_URL":      "",
		"TEST_DATABASE_URL": "postgres://u:p@localhost:5434/credit_test",
	})))
	require.NoError(t, err)
	require.Equal(t, "postgres://u:p@localhost:5434/credit_test", cfg.Database.URL)
}

func TestLoadRejectsInvalidValuesWithoutLeakingThem(t *testing.T) {
	_, err := LoadFrom(lookupFrom(validEnv(map[string]string{
		"DATABASE_URL":      "mysql://secret-user:secret-pass@host/db",
		"HTTP_PORT":         "99999",
		"DATABASE_POOL_MIN": "5",
		"DATABASE_POOL_MAX": "2",
	})))
	require.Error(t, err)
	require.ErrorContains(t, err, "DATABASE_URL must be a PostgreSQL URL")
	require.ErrorContains(t, err, "HTTP_PORT")
	require.ErrorContains(t, err, "DATABASE_POOL_MIN must not exceed")
	require.NotContains(t, err.Error(), "secret-pass")
}

func TestLoadRequiresInitialCreditAndAuth0Issuer(t *testing.T) {
	_, err := LoadFrom(lookupFrom(map[string]string{
		"DATABASE_URL": "postgres://u:p@localhost:5432/credit",
	}))
	require.ErrorContains(t, err, "INITIAL_CREDIT_AMOUNT is required")
	require.ErrorContains(t, err, "AUTH0_ISSUER")
}

func TestInitialCreditIsBounded(t *testing.T) {
	for _, value := range []string{"-1", "1.5", "abc", "1000001"} {
		_, err := LoadFrom(lookupFrom(validEnv(map[string]string{"INITIAL_CREDIT_AMOUNT": value})))
		require.ErrorContains(t, err, "INITIAL_CREDIT_AMOUNT must be an integer", value)
	}
	cfg, err := LoadFrom(lookupFrom(validEnv(map[string]string{"INITIAL_CREDIT_AMOUNT": "0"})))
	require.NoError(t, err)
	require.Zero(t, cfg.InitialCredit)
}
