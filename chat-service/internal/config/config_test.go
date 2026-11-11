// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-10
// Scope: Implementation of test code (unit and integration tests).
// Author review: Reviewed, validated, and tested by the authors.

package config

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/auth"
)

func lookupFrom(values map[string]string) LookupEnv {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func validEnv(extra map[string]string) map[string]string {
	env := map[string]string{
		"DATABASE_URL":       "postgresql://chat_service:db-secret@chat-db:5432/chat_service?sslmode=disable",
		"AMQP_URL":           "amqp://foc:amqp-secret@queue-service:5672/",
		"AUTH0_ISSUER":       "https://example.auth0.com/",
		"WS_ALLOWED_ORIGINS": "localhost:8081, localhost:5173",
	}
	for key, value := range extra {
		env[key] = value
	}
	return env
}

func TestLoadAppliesDefaults(t *testing.T) {
	cfg, err := LoadFrom(lookupFrom(validEnv(nil)))
	require.NoError(t, err)

	require.Equal(t, Development, cfg.Environment)
	require.Equal(t, "0.0.0.0:3006", cfg.HTTP.Address())
	require.Equal(t, "info", cfg.LogLevel)
	require.Equal(t, 30*time.Second, cfg.StartupTimeout)
	require.Equal(t, 10*time.Second, cfg.ShutdownTimeout)
	require.Equal(t, DatabaseConfig{
		URL:            "postgresql://chat_service:db-secret@chat-db:5432/chat_service?sslmode=disable",
		MinConnections: 1,
		MaxConnections: 10,
	}, cfg.Database)
	wantAuth := auth.DefaultAuth0Config()
	wantAuth.Issuer = "https://example.auth0.com/"
	require.Equal(t, wantAuth, cfg.Auth)
	require.Equal(t, "https://api.foc.local/chat-service", cfg.Auth.Audience)
	require.Equal(t, AMQPConfig{URL: "amqp://foc:amqp-secret@queue-service:5672/", Prefetch: 10}, cfg.AMQP)
	require.Equal(t, ChatConfig{
		CloseDelay:       24 * time.Hour,
		MaxMessageLength: 2000,
		SendRate:         5,
		SendBurst:        10,
	}, cfg.Chat)
	require.Equal(t, WebSocketConfig{
		AllowedOrigins:        []string{"localhost:8081", "localhost:5173"},
		AuthTimeout:           5 * time.Second,
		PingInterval:          25 * time.Second,
		MaxConnectionsPerUser: 5,
	}, cfg.WebSocket)
	require.Empty(t, cfg.RedisURL)
}

func TestLoadReadsOverrides(t *testing.T) {
	cfg, err := LoadFrom(lookupFrom(validEnv(map[string]string{
		"APP_ENV":                     "production",
		"HTTP_HOST":                   "127.0.0.1",
		"HTTP_PORT":                   "4005",
		"LOG_LEVEL":                   "debug",
		"AUTH0_AUDIENCE":              "https://api.example.com/chat",
		"CHAT_CLOSE_DELAY":            "1m",
		"CHAT_MAX_MESSAGE_LENGTH":     "500",
		"CHAT_SEND_RATE":              "0.5",
		"CHAT_SEND_BURST":             "3",
		"WS_ALLOWED_ORIGINS":          "app.bestfoc.app",
		"WS_AUTH_TIMEOUT":             "2s",
		"WS_PING_INTERVAL":            "10s",
		"WS_MAX_CONNECTIONS_PER_USER": "2",
		"REDIS_URL":                   "rediss://cache.example.com:6379/0",
	})))
	require.NoError(t, err)

	require.Equal(t, Production, cfg.Environment)
	require.Equal(t, "127.0.0.1:4005", cfg.HTTP.Address())
	require.Equal(t, "https://api.example.com/chat", cfg.Auth.Audience)
	require.Equal(t, ChatConfig{CloseDelay: time.Minute, MaxMessageLength: 500, SendRate: 0.5, SendBurst: 3}, cfg.Chat)
	require.Equal(t, WebSocketConfig{
		AllowedOrigins:        []string{"app.bestfoc.app"},
		AuthTimeout:           2 * time.Second,
		PingInterval:          10 * time.Second,
		MaxConnectionsPerUser: 2,
	}, cfg.WebSocket)
	require.Equal(t, "rediss://cache.example.com:6379/0", cfg.RedisURL)
}

// The plan's "done when" for config: several problems in one error, and no
// configured value in it.
func TestLoadReportsEveryProblemAtOnceWithoutValues(t *testing.T) {
	env := validEnv(map[string]string{
		"HTTP_PORT": "not-a-port",
		"AMQP_URL":  "http://foc:amqp-secret@queue-service:5672/",
		"REDIS_URL": "http://user:redis-secret@cache:6379",
	})
	delete(env, "DATABASE_URL")

	_, err := LoadFrom(lookupFrom(env))
	require.Error(t, err)
	var configErr *Error
	require.True(t, errors.As(err, &configErr))
	require.Equal(t, []string{
		"HTTP_PORT must be an integer between 1 and 65535",
		"DATABASE_URL is required",
		"AMQP_URL must be an amqp:// or amqps:// URL",
		"REDIS_URL must be a redis:// or rediss:// URL, or empty",
	}, configErr.Problems())
	for _, secret := range []string{"amqp-secret", "redis-secret", "db-secret", "queue-service", "cache:6379"} {
		require.NotContains(t, err.Error(), secret)
	}
}

func TestLoadRejectsANonPostgresDatabaseURLWithoutLeakingIt(t *testing.T) {
	_, err := LoadFrom(lookupFrom(validEnv(map[string]string{
		"DATABASE_URL": "mysql://chat:db-secret@db:3306/chat",
	})))
	require.ErrorContains(t, err, "DATABASE_URL must be a PostgreSQL URL")
	require.NotContains(t, err.Error(), "db-secret")
}

func TestTestEnvironmentReadsTestDatabaseURL(t *testing.T) {
	_, err := LoadFrom(lookupFrom(validEnv(map[string]string{"APP_ENV": "test"})))
	require.ErrorContains(t, err, "TEST_DATABASE_URL is required")

	cfg, err := LoadFrom(lookupFrom(validEnv(map[string]string{
		"APP_ENV":           "test",
		"TEST_DATABASE_URL": "postgres://chat_test@127.0.0.1:5435/chat_test?sslmode=disable",
	})))
	require.NoError(t, err)
	require.Equal(t, "postgres://chat_test@127.0.0.1:5435/chat_test?sslmode=disable", cfg.Database.URL)
}

func TestLoadRequiresServiceDependencies(t *testing.T) {
	_, err := LoadFrom(lookupFrom(map[string]string{}))
	require.Error(t, err)
	for _, want := range []string{
		"DATABASE_URL is required",
		"AMQP_URL is required",
		"AUTH0_ISSUER is required",
		"WS_ALLOWED_ORIGINS is required",
	} {
		require.ErrorContains(t, err, want)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		key, value, want string
	}{
		{"APP_ENV", "staging", "APP_ENV must be development, test, or production"},
		{"LOG_LEVEL", "verbose", "LOG_LEVEL must be"},
		{"HTTP_PORT", "70000", "HTTP_PORT must be an integer"},
		{"STARTUP_TIMEOUT", "0s", "STARTUP_TIMEOUT must be a positive duration"},
		{"SHUTDOWN_TIMEOUT", "soon", "SHUTDOWN_TIMEOUT must be a positive duration"},
		{"DATABASE_POOL_MAX", "0", "DATABASE_POOL_MAX must be an integer"},
		{"DATABASE_POOL_MIN", "20", "DATABASE_POOL_MIN must not exceed DATABASE_POOL_MAX"},
		{"AUTH0_ISSUER", "http://example.auth0.com/", "AUTH0_ISSUER must be an HTTPS origin"},
		{"AUTH0_ISSUER", "https://example.auth0.com", "AUTH0_ISSUER must be an HTTPS origin"},
		{"AUTH0_AUDIENCE", "a,b", "AUTH0_AUDIENCE must be"},
		{"AUTH0_CLOCK_SKEW", "5m", "AUTH0_CLOCK_SKEW must be between 0s and 1m"},
		{"AUTH0_JWKS_FETCH_ATTEMPTS", "9", "AUTH0_JWKS_FETCH_ATTEMPTS must be an integer between 1 and 3"},
		{"AMQP_PREFETCH", "0", "AMQP_PREFETCH must be an integer between 1 and 1000"},
		{"CHAT_CLOSE_DELAY", "-1h", "CHAT_CLOSE_DELAY must be a positive duration"},
		{"CHAT_MAX_MESSAGE_LENGTH", "2001", "CHAT_MAX_MESSAGE_LENGTH must be an integer between 1 and 2000"},
		{"CHAT_SEND_RATE", "0", "CHAT_SEND_RATE must be a number above 0"},
		{"CHAT_SEND_RATE", "5/s", "CHAT_SEND_RATE must be a number"},
		{"CHAT_SEND_BURST", "0", "CHAT_SEND_BURST must be an integer"},
		{"WS_ALLOWED_ORIGINS", "https://app.bestfoc.app", "WS_ALLOWED_ORIGINS must list host patterns"},
		{"WS_ALLOWED_ORIGINS", "app.bestfoc.app/chat", "WS_ALLOWED_ORIGINS must list host patterns"},
		{"WS_ALLOWED_ORIGINS", "[localhost", "WS_ALLOWED_ORIGINS contains a malformed pattern"},
		{"WS_ALLOWED_ORIGINS", " , ", "WS_ALLOWED_ORIGINS is required"},
		{"WS_AUTH_TIMEOUT", "0", "WS_AUTH_TIMEOUT must be a positive duration"},
		{"WS_PING_INTERVAL", "60s", "WS_PING_INTERVAL must be shorter than 1m"},
		{"WS_MAX_CONNECTIONS_PER_USER", "0", "WS_MAX_CONNECTIONS_PER_USER must be an integer"},
	}
	for _, tt := range tests {
		t.Run(tt.key+"="+tt.value, func(t *testing.T) {
			_, err := LoadFrom(lookupFrom(validEnv(map[string]string{tt.key: tt.value})))
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestLoadAcceptsWildcardOriginPatterns(t *testing.T) {
	cfg, err := LoadFrom(lookupFrom(validEnv(map[string]string{
		"WS_ALLOWED_ORIGINS": "*.bestfoc.app,localhost:*",
	})))
	require.NoError(t, err)
	require.Equal(t, []string{"*.bestfoc.app", "localhost:*"}, cfg.WebSocket.AllowedOrigins)
}
