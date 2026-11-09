package config

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func env(overrides map[string]string) LookupEnv {
	values := map[string]string{
		"DATABASE_URL":        "postgres://admin_service:pw@db:5432/admin_service",
		"AUTH0_ISSUER":        "https://tenant.auth0.com/",
		"ADMIN_M2M_CLIENT_ID": "client", "ADMIN_M2M_CLIENT_SECRET": "s3cret",
		"ORDER_SERVICE_URL": "http://order-service:3003/",
		"AMQP_URL":          "amqp://guest:guest@mq:5672/",
	}
	for k, v := range overrides {
		values[k] = v
	}
	return func(key string) (string, bool) {
		v, ok := values[key]
		return v, ok
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := LoadFrom(env(nil))

	require.NoError(t, err)
	require.Equal(t, Development, cfg.Environment)
	require.Equal(t, "0.0.0.0:3005", cfg.HTTP.Address())
	require.Equal(t, "https://api.foc.local/admin-service", cfg.Auth.Audience)
	require.Equal(t, "https://api.foc.local/order-service", cfg.Order.Credentials.Audience)
	require.Equal(t, "http://order-service:3003", cfg.Order.BaseURL, "no trailing slash")
	require.Equal(t, 5*time.Second, cfg.Order.Timeout)
	require.Equal(t, 16, cfg.AMQP.Prefetch)
	require.Equal(t, "Asia/Singapore", cfg.Timezone.String())
	require.Equal(t, ReadinessConfig{Timeout: 2 * time.Second}, cfg.Readiness, "no other service is checked until configured")
}

func TestLoadReadinessTargets(t *testing.T) {
	cfg, err := LoadFrom(env(map[string]string{
		"SUPPLIER_SERVICE_URL": "http://supplier-service:3002/", "CREDIT_SERVICE_URL": "http://credit-service:3004",
		"USER_SERVICE_URL": "http://user-service:8080/", "READINESS_TIMEOUT": "750ms",
	}))

	require.NoError(t, err)
	require.Equal(t, ReadinessConfig{
		SupplierURL: "http://supplier-service:3002", CreditURL: "http://credit-service:3004",
		UserURL: "http://user-service:8080", Timeout: 750 * time.Millisecond,
	}, cfg.Readiness, "no trailing slash")

	_, err = LoadFrom(env(map[string]string{"SUPPLIER_SERVICE_URL": "supplier", "USER_SERVICE_URL": "ftp://user", "READINESS_TIMEOUT": "soon"}))
	require.ErrorContains(t, err, "SUPPLIER_SERVICE_URL must be an absolute http(s) URL, or empty")
	require.ErrorContains(t, err, "USER_SERVICE_URL must be an absolute http(s) URL, or empty")
	require.ErrorContains(t, err, "READINESS_TIMEOUT must be a positive duration")
}

func TestLoadTestEnvironmentReadsTheTestDatabase(t *testing.T) {
	cfg, err := LoadFrom(env(map[string]string{
		"APP_ENV": "test", "TEST_DATABASE_URL": "postgres://t@localhost/test", "DATABASE_URL": "",
	}))

	require.NoError(t, err)
	require.Equal(t, "postgres://t@localhost/test", cfg.Database.URL)
}

func TestLoadReportsEveryProblemWithoutEchoingValues(t *testing.T) {
	_, err := LoadFrom(func(key string) (string, bool) {
		return map[string]string{
				"APP_ENV": "staging", "HTTP_PORT": "99999", "DATABASE_URL": "mysql://admin:topsecret@db/x",
				"AUTH0_ISSUER": "http://insecure.example/", "ORDER_SERVICE_URL": "ftp://order",
				"AMQP_URL": "http://not-amqp", "DASHBOARD_TIMEZONE": "Mars/Olympus", "EVENTS_PREFETCH": "0",
				"ADMIN_M2M_CLIENT_ID": "client", "ADMIN_M2M_CLIENT_SECRET": "topsecret",
			}[key], key != "" && map[string]bool{
				"APP_ENV": true, "HTTP_PORT": true, "DATABASE_URL": true, "AUTH0_ISSUER": true, "ORDER_SERVICE_URL": true,
				"AMQP_URL": true, "DASHBOARD_TIMEZONE": true, "EVENTS_PREFETCH": true, "ADMIN_M2M_CLIENT_ID": true,
				"ADMIN_M2M_CLIENT_SECRET": true,
			}[key]
	})

	require.Error(t, err)
	for _, want := range []string{
		"APP_ENV must be", "HTTP_PORT must be", "DATABASE_URL must be a PostgreSQL URL", "AUTH0_ISSUER must be",
		"ORDER_SERVICE_URL must be an absolute http(s) URL", "AMQP_URL must be", "DASHBOARD_TIMEZONE must be", "EVENTS_PREFETCH must be",
	} {
		require.Contains(t, err.Error(), want)
	}
	require.NotContains(t, err.Error(), "topsecret", "no configured value is echoed")
	require.False(t, strings.Contains(err.Error(), "admin:"))
}

func TestLoadRequiresTheEssentials(t *testing.T) {
	_, err := LoadFrom(func(string) (string, bool) { return "", false })

	require.Error(t, err)
	for _, key := range []string{"DATABASE_URL", "AUTH0_ISSUER", "ADMIN_M2M_CLIENT_ID", "ADMIN_M2M_CLIENT_SECRET", "ORDER_SERVICE_URL", "AMQP_URL"} {
		require.Contains(t, err.Error(), key+" is required")
	}
}

func TestLoadTimezoneOverride(t *testing.T) {
	cfg, err := LoadFrom(env(map[string]string{"DASHBOARD_TIMEZONE": "UTC"}))

	require.NoError(t, err)
	require.Equal(t, "UTC", cfg.Timezone.String())
}
