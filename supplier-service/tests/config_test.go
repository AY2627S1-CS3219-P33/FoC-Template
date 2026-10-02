package tests

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/config"
)

func TestConfigurationUsesIsolatedTestDatabase(t *testing.T) {
	testDatabaseURL := "postgresql://supplier_test@127.0.0.1:5433/supplier_test"
	values := map[string]string{
		"SUPPLIER_SEED_CSV_PATH": "../data/csv/supplier-seed-data.csv",
		"AUTH0_ISSUER":           "https://tenant.example.com/",
		"APP_ENV":                "test",
		"TEST_DATABASE_URL":      testDatabaseURL,
	}

	cfg, err := config.LoadFrom(mapLookup(values))

	require.NoError(t, err)
	require.Equal(t, config.Test, cfg.Environment)
	require.Equal(t, testDatabaseURL, cfg.Database.URL)
	require.Equal(t, "template-v1", cfg.SeedNamespace)
	require.Equal(t, "../data/csv/supplier-seed-data.csv", cfg.SeedCSVPath)
	require.Equal(t, 3002, cfg.HTTP.Port)
}

func TestConfigurationAcceptsSeedDatasetNamespace(t *testing.T) {
	values := map[string]string{
		"SUPPLIER_SEED_CSV_PATH":          "/deployment/suppliers.csv",
		"AUTH0_ISSUER":                    "https://tenant.example.com/",
		"APP_ENV":                         "test",
		"TEST_DATABASE_URL":               "postgresql://supplier_test@127.0.0.1:5433/supplier_test",
		"SUPPLIER_SEED_DATASET_NAMESPACE": "deployment-v2",
	}

	cfg, err := config.LoadFrom(mapLookup(values))

	require.NoError(t, err)
	require.Equal(t, "deployment-v2", cfg.SeedNamespace)
}

func TestConfigurationRejectsMissingTestDatabaseWithoutLeakingValues(t *testing.T) {
	values := map[string]string{
		"AUTH0_ISSUER": "https://tenant.example.com/",
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

func TestAuth0Configuration_NFR3_4(t *testing.T) {
	base := map[string]string{"APP_ENV": "test", "TEST_DATABASE_URL": "postgresql://test@localhost/test", "AUTH0_ISSUER": "https://tenant.example.com/", "SUPPLIER_SEED_CSV_PATH": "/deployment/suppliers.csv"}
	cfg, err := config.LoadFrom(mapLookup(base))
	require.NoError(t, err)
	require.Equal(t, "https://api.foc.local/supplier-service", cfg.Auth.Audience)
	for _, tt := range []struct{ key, value string }{
		{"AUTH0_ISSUER", ""}, {"AUTH0_ISSUER", "http://tenant.example.com/"}, {"AUTH0_ISSUER", "https://tenant.example.com"},
		{"AUTH0_ISSUER", "https://secret-user:secret-password@tenant.example.com/"},
		{"AUTH0_ISSUER", "https://tenant.example.com/?secret-password"},
		{"AUTH0_JWKS_CACHE_TTL", "0s"}, {"AUTH0_JWKS_CACHE_TTL", "2h"},
		{"AUTH0_CLOCK_SKEW", "-1s"}, {"AUTH0_CLOCK_SKEW", "2m"}, {"AUTH0_CLOCK_SKEW", "bad"},
		{"AUTH0_JWKS_FETCH_TIMEOUT", "11s"}, {"AUTH0_JWKS_REFRESH_INTERVAL", "6m"},
		{"AUTH0_JWKS_FETCH_ATTEMPTS", "4"}, {"AUTH0_JWKS_BREAKER_THRESHOLD", "0"},
		{"AUTH0_JWKS_BREAKER_COOLDOWN", "-1s"}, {"AUTH0_AUDIENCE", "https://api.foc.local/supplier-service,"},
		{"SUPPLIER_SEED_CSV_PATH", ""}, {"SUPPLIER_SEED_CSV_PATH", "   "},
		{"STARTUP_TIMEOUT", "0s"}, {"STARTUP_TIMEOUT", "invalid"},
	} {
		t.Run(tt.key+" "+tt.value, func(t *testing.T) {
			values := map[string]string{}
			for k, v := range base {
				values[k] = v
			}
			values[tt.key] = tt.value
			_, err := config.LoadFrom(mapLookup(values))
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.key)
			require.NotContains(t, err.Error(), "secret-password")
		})
	}
	base["AUTH0_CLOCK_SKEW"] = "0s"
	_, err = config.LoadFrom(mapLookup(base))
	require.NoError(t, err)
}
