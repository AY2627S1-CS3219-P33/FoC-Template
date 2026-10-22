package config

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func validEnv() map[string]string {
	return map[string]string{
		"DATABASE_URL":            "postgresql://order_service:secret@order-db:5432/order_service?sslmode=disable",
		"AUTH0_ISSUER":            "https://tenant.auth0.com/",
		"ORDER_M2M_CLIENT_ID":     "client-id",
		"ORDER_M2M_CLIENT_SECRET": "client-secret",
		"SUPPLIER_SERVICE_URL":    "http://supplier-service:3002/",
		"AMQP_URL":                "amqp://foc:secret@queue-service:5672/",
	}
}

func lookupFrom(env map[string]string) LookupEnv {
	return func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	}
}

func TestLoadFromAppliesDefaults(t *testing.T) {
	cfg, err := LoadFrom(lookupFrom(validEnv()))
	require.NoError(t, err)

	require.Equal(t, Development, cfg.Environment)
	require.Equal(t, "0.0.0.0:3003", cfg.HTTP.Address())
	require.Equal(t, "info", cfg.LogLevel)
	require.Equal(t, 10*time.Second, cfg.ShutdownTimeout)
	require.Equal(t, int32(1), cfg.Database.MinConnections)
	require.Equal(t, int32(10), cfg.Database.MaxConnections)
	require.Equal(t, "https://api.foc.local/order-service", cfg.Auth.Audience)
	require.Equal(t, "http://supplier-service:3002", cfg.Supplier.BaseURL)
	require.Equal(t, 2*time.Second, cfg.Supplier.Timeout)
	require.Equal(t, cfg.Auth.Issuer, cfg.Supplier.Credentials.Issuer)
	require.Equal(t, SupplierAudience, cfg.Supplier.Credentials.Audience)
	require.Empty(t, cfg.RedisURL, "an unset REDIS_URL selects the no-op cache")
	require.False(t, cfg.AMQP.DeclareTap)
	require.Equal(t, 16, cfg.AMQP.Prefetch)
}

func TestLoadFromRequiresAMQPURL(t *testing.T) {
	env := validEnv()
	delete(env, "AMQP_URL")

	_, err := LoadFrom(lookupFrom(env))
	require.ErrorContains(t, err, "AMQP_URL is required")
}

func TestLoadFromReadsOptionalValues(t *testing.T) {
	env := validEnv()
	env["REDIS_URL"] = "redis://order-redis:6379/0"
	env["AMQP_DECLARE_TAP"] = "true"
	env["CREDIT_RESULTS_PREFETCH"] = "4"
	env["AUTH0_CLOCK_SKEW"] = "0s"

	cfg, err := LoadFrom(lookupFrom(env))
	require.NoError(t, err)
	require.Equal(t, "redis://order-redis:6379/0", cfg.RedisURL)
	require.True(t, cfg.AMQP.DeclareTap)
	require.Equal(t, 4, cfg.AMQP.Prefetch)
	require.Zero(t, cfg.Auth.ClockSkew)
}

func TestLoadFromUsesTestDatabaseInTestEnvironment(t *testing.T) {
	env := validEnv()
	env["APP_ENV"] = "test"
	delete(env, "DATABASE_URL")

	_, err := LoadFrom(lookupFrom(env))
	require.ErrorContains(t, err, "TEST_DATABASE_URL is required")

	env["TEST_DATABASE_URL"] = "postgres://order_test@127.0.0.1:5434/order_test?sslmode=disable"
	cfg, err := LoadFrom(lookupFrom(env))
	require.NoError(t, err)
	require.Equal(t, env["TEST_DATABASE_URL"], cfg.Database.URL)
}

func TestLoadFromReportsEveryProblemWithoutValues(t *testing.T) {
	env := map[string]string{
		"APP_ENV":          "staging",
		"HTTP_PORT":        "0",
		"DATABASE_URL":     "mysql://user:hunter2@db/order",
		"AUTH0_ISSUER":     "http://tenant.auth0.com",
		"REDIS_URL":        "memcached://cache",
		"AMQP_URL":         "http://user:hunter2@broker/",
		"AMQP_DECLARE_TAP": "fakse",
	}

	_, err := LoadFrom(lookupFrom(env))
	require.Error(t, err)
	message := err.Error()
	for _, want := range []string{
		"APP_ENV", "HTTP_PORT", "DATABASE_URL must be a PostgreSQL URL", "AUTH0_ISSUER",
		"ORDER_M2M_CLIENT_ID is required", "ORDER_M2M_CLIENT_SECRET is required",
		"SUPPLIER_SERVICE_URL is required", "REDIS_URL", "AMQP_URL must be", "AMQP_DECLARE_TAP",
	} {
		require.Contains(t, message, want)
	}
	require.False(t, strings.Contains(message, "hunter2"), "errors must not echo configured values")
}
