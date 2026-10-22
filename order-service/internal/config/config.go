// Package config reads order-service's environment. Every problem is
// collected and reported at once, and no error includes a configured value,
// because URLs and the client secret may carry credentials.
package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/serviceauth"
)

type Environment string

const (
	Development Environment = "development"
	Test        Environment = "test"
	Production  Environment = "production"
)

// SupplierAudience is supplier-service's Auth0 API identifier.
const SupplierAudience = "https://api.foc.local/supplier-service"

type HTTPConfig struct {
	Host string
	Port int
}

func (c HTTPConfig) Address() string {
	return net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
}

type DatabaseConfig struct {
	URL            string
	MinConnections int32
	MaxConnections int32
}

// SupplierConfig is how order-service calls supplier-service.
type SupplierConfig struct {
	// BaseURL has no trailing slash.
	BaseURL string
	Timeout time.Duration
	// Credentials obtain the machine-to-machine token for supplier-service's
	// audience.
	Credentials serviceauth.Config
}

// AMQPConfig is the RabbitMQ connection. There is no broker-less mode:
// credit work cannot happen without it.
type AMQPConfig struct {
	URL string
	// DeclareTap binds order-events.tap to errand.# so events can be
	// inspected before a notification consumer exists.
	DeclareTap bool
	// Prefetch bounds unacknowledged credit results; zero means the
	// consumer's default.
	Prefetch int
}

type Config struct {
	Environment     Environment
	HTTP            HTTPConfig
	LogLevel        string
	StartupTimeout  time.Duration
	ShutdownTimeout time.Duration
	Database        DatabaseConfig
	Auth            auth.Auth0Config
	Supplier        SupplierConfig
	// RedisURL is optional; empty selects the no-op caches.
	RedisURL string
	AMQP     AMQPConfig
}

type LookupEnv func(string) (string, bool)

type Error struct {
	problems []string
}

func (e *Error) Error() string {
	return "invalid environment configuration: " + strings.Join(e.problems, "; ")
}

func Load() (Config, error) {
	return LoadFrom(os.LookupEnv)
}

func LoadFrom(lookup LookupEnv) (Config, error) {
	var problems []string

	environment := Environment(read(lookup, "APP_ENV", string(Development)))
	if environment != Development && environment != Test && environment != Production {
		problems = append(problems, "APP_ENV must be development, test, or production")
		environment = Development
	}

	host := read(lookup, "HTTP_HOST", "0.0.0.0")
	port := readInt(lookup, "HTTP_PORT", 3003, 1, 65_535, &problems)
	logLevel := read(lookup, "LOG_LEVEL", "info")
	if !validLogLevel(logLevel) {
		problems = append(problems, "LOG_LEVEL must be debug, info, warn, or error")
	}
	startupTimeout := readDuration(lookup, "STARTUP_TIMEOUT", 30*time.Second, &problems)
	shutdownTimeout := readDuration(lookup, "SHUTDOWN_TIMEOUT", 10*time.Second, &problems)

	databaseKey := "DATABASE_URL"
	if environment == Test {
		databaseKey = "TEST_DATABASE_URL"
	}
	databaseURL := required(lookup, databaseKey, &problems)
	if databaseURL != "" && !validURL(databaseURL, "postgres", "postgresql") {
		problems = append(problems, databaseKey+" must be a PostgreSQL URL")
	}
	poolMin := readInt(lookup, "DATABASE_POOL_MIN", 1, 0, 1_000, &problems)
	poolMax := readInt(lookup, "DATABASE_POOL_MAX", 10, 1, 1_000, &problems)
	if poolMin > poolMax {
		problems = append(problems, "DATABASE_POOL_MIN must not exceed DATABASE_POOL_MAX")
	}

	authConfig := auth.DefaultAuth0Config()
	authConfig.Issuer = strings.TrimSpace(required(lookup, "AUTH0_ISSUER", &problems))
	authConfig.Audience = read(lookup, "AUTH0_AUDIENCE", authConfig.Audience)
	authConfig.ClockSkew = readDurationAllowZero(lookup, "AUTH0_CLOCK_SKEW", authConfig.ClockSkew, &problems)
	authConfig.CacheTTL = readDuration(lookup, "AUTH0_JWKS_CACHE_TTL", authConfig.CacheTTL, &problems)
	authConfig.FetchTimeout = readDuration(lookup, "AUTH0_JWKS_FETCH_TIMEOUT", authConfig.FetchTimeout, &problems)
	authConfig.RefreshInterval = readDuration(lookup, "AUTH0_JWKS_REFRESH_INTERVAL", authConfig.RefreshInterval, &problems)
	authConfig.FetchAttempts = readInt(lookup, "AUTH0_JWKS_FETCH_ATTEMPTS", authConfig.FetchAttempts, 1, 3, &problems)
	authConfig.BreakerThreshold = readInt(lookup, "AUTH0_JWKS_BREAKER_THRESHOLD", authConfig.BreakerThreshold, 1, 100, &problems)
	authConfig.BreakerCooldown = readDuration(lookup, "AUTH0_JWKS_BREAKER_COOLDOWN", authConfig.BreakerCooldown, &problems)
	if authConfig.Issuer != "" {
		if err := authConfig.Validate(); err != nil {
			problems = append(problems, err.Error())
		}
	}

	// The M2M application belongs to the same Auth0 tenant that issues
	// order-service's own tokens.
	credentials := serviceauth.Config{
		Issuer:       authConfig.Issuer,
		ClientID:     strings.TrimSpace(required(lookup, "ORDER_M2M_CLIENT_ID", &problems)),
		ClientSecret: required(lookup, "ORDER_M2M_CLIENT_SECRET", &problems),
		Audience:     read(lookup, "SUPPLIER_AUDIENCE", SupplierAudience),
	}
	if authConfig.Issuer != "" && credentials.ClientID != "" && credentials.ClientSecret != "" {
		if err := credentials.Validate(); err != nil {
			problems = append(problems, "SUPPLIER_AUDIENCE must be a nonempty API identifier")
		}
	}
	supplierURL := strings.TrimRight(strings.TrimSpace(required(lookup, "SUPPLIER_SERVICE_URL", &problems)), "/")
	if supplierURL != "" && !validURL(supplierURL, "http", "https") {
		problems = append(problems, "SUPPLIER_SERVICE_URL must be an absolute http(s) URL")
	}
	supplierTimeout := readDuration(lookup, "SUPPLIER_SERVICE_TIMEOUT", 2*time.Second, &problems)

	redisURL := strings.TrimSpace(read(lookup, "REDIS_URL", ""))
	if redisURL != "" && !validURL(redisURL, "redis", "rediss") {
		problems = append(problems, "REDIS_URL must be a redis:// or rediss:// URL, or empty")
	}

	amqpURL := strings.TrimSpace(required(lookup, "AMQP_URL", &problems))
	if amqpURL != "" && !validURL(amqpURL, "amqp", "amqps") {
		problems = append(problems, "AMQP_URL must be an amqp:// or amqps:// URL")
	}
	declareTap := readBool(lookup, "AMQP_DECLARE_TAP", false, &problems)
	prefetch := readInt(lookup, "CREDIT_RESULTS_PREFETCH", 16, 1, 1_000, &problems)

	if len(problems) > 0 {
		return Config{}, &Error{problems: problems}
	}

	return Config{
		Environment:     environment,
		HTTP:            HTTPConfig{Host: host, Port: port},
		LogLevel:        logLevel,
		StartupTimeout:  startupTimeout,
		ShutdownTimeout: shutdownTimeout,
		Database: DatabaseConfig{
			URL:            databaseURL,
			MinConnections: int32(poolMin),
			MaxConnections: int32(poolMax),
		},
		Auth: authConfig,
		Supplier: SupplierConfig{
			BaseURL:     supplierURL,
			Timeout:     supplierTimeout,
			Credentials: credentials,
		},
		RedisURL: redisURL,
		AMQP:     AMQPConfig{URL: amqpURL, DeclareTap: declareTap, Prefetch: prefetch},
	}, nil
}

func read(lookup LookupEnv, key, fallback string) string {
	value, found := lookup(key)
	if !found || strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func required(lookup LookupEnv, key string, problems *[]string) string {
	value, found := lookup(key)
	if !found || strings.TrimSpace(value) == "" {
		*problems = append(*problems, key+" is required")
		return ""
	}
	return value
}

func readInt(
	lookup LookupEnv,
	key string,
	fallback, minimum, maximum int,
	problems *[]string,
) int {
	value := read(lookup, key, strconv.Itoa(fallback))
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum || parsed > maximum {
		*problems = append(
			*problems,
			fmt.Sprintf("%s must be an integer between %d and %d", key, minimum, maximum),
		)
		return fallback
	}
	return parsed
}

func readDuration(
	lookup LookupEnv,
	key string,
	fallback time.Duration,
	problems *[]string,
) time.Duration {
	value := read(lookup, key, fallback.String())
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		*problems = append(*problems, key+" must be a positive duration")
		return fallback
	}
	return parsed
}

// readDurationAllowZero is readDuration for values where 0s is meaningful.
func readDurationAllowZero(
	lookup LookupEnv,
	key string,
	fallback time.Duration,
	problems *[]string,
) time.Duration {
	value := read(lookup, key, fallback.String())
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed < 0 {
		*problems = append(*problems, key+" must be a duration")
		return fallback
	}
	return parsed
}

func readBool(lookup LookupEnv, key string, fallback bool, problems *[]string) bool {
	value := read(lookup, key, strconv.FormatBool(fallback))
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		*problems = append(*problems, key+" must be true or false")
		return fallback
	}
	return parsed
}

func validURL(value string, schemes ...string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" {
		return false
	}
	for _, scheme := range schemes {
		if parsed.Scheme == scheme {
			return true
		}
	}
	return false
}

func validLogLevel(level string) bool {
	switch level {
	case "debug", "info", "warn", "error":
		return true
	default:
		return false
	}
}
