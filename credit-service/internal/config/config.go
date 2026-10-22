// Package config loads and validates service settings from environment variables.
package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/credit-service/internal/auth"
)

// MaxInitialCredit bounds INITIAL_CREDIT_AMOUNT so a typo cannot mint a
// huge balance for every new user.
const MaxInitialCredit = 1_000_000

type Environment string

const (
	Development Environment = "development"
	Test        Environment = "test"
	Production  Environment = "production"
)

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

// MessagingConfig holds the RabbitMQ settings: the consumer of order-service's
// commands and the dispatcher that publishes the results.
type MessagingConfig struct {
	// URL is the AMQP URL, including credentials. It is never logged.
	URL string
	// ConsumerPrefetch bounds the unacknowledged commands the broker sends ahead.
	ConsumerPrefetch int
	// OutboxPollInterval is how long the dispatcher waits when no result is due.
	OutboxPollInterval time.Duration
	// OutboxBatchSize is the most results the dispatcher claims at once.
	OutboxBatchSize int
	// OutboxMaxAttempts is how often a result is published before it is given up.
	OutboxMaxAttempts int
}

type Config struct {
	Auth auth.Auth0Config
	// InitialCredit is the whole number of credits every new wallet receives.
	// It lives here, not in requests, so no caller can choose how many credits
	// to create.
	InitialCredit   int64
	Environment     Environment
	HTTP            HTTPConfig
	Database        DatabaseConfig
	Messaging       MessagingConfig
	StartupTimeout  time.Duration
	LogLevel        string
	ShutdownTimeout time.Duration
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
	port := readInt(lookup, "HTTP_PORT", 3004, 1, 65_535, &problems)
	logLevel := read(lookup, "LOG_LEVEL", "info")
	if !validLogLevel(logLevel) {
		problems = append(problems, "LOG_LEVEL must be debug, info, warn, or error")
	}
	shutdownTimeout := readDuration(lookup, "SHUTDOWN_TIMEOUT", 10*time.Second, &problems)
	startupTimeout := readDuration(lookup, "STARTUP_TIMEOUT", 30*time.Second, &problems)

	databaseKey := "DATABASE_URL"
	if environment == Test {
		databaseKey = "TEST_DATABASE_URL"
	}
	databaseURL := required(lookup, databaseKey, &problems)
	if databaseURL != "" && !validPostgreSQLURL(databaseURL) {
		problems = append(problems, databaseKey+" must be a PostgreSQL URL")
	}

	poolMin := readInt(lookup, "DATABASE_POOL_MIN", 1, 0, 1_000, &problems)
	poolMax := readInt(lookup, "DATABASE_POOL_MAX", 10, 1, 1_000, &problems)
	if poolMin > poolMax {
		problems = append(problems, "DATABASE_POOL_MIN must not exceed DATABASE_POOL_MAX")
	}

	amqpURL := required(lookup, "AMQP_URL", &problems)
	if amqpURL != "" && !validAMQPURL(amqpURL) {
		problems = append(problems, "AMQP_URL must be an amqp or amqps URL")
	}
	messaging := MessagingConfig{
		URL:                amqpURL,
		ConsumerPrefetch:   readInt(lookup, "AMQP_PREFETCH", 16, 1, 1_000, &problems),
		OutboxPollInterval: readDuration(lookup, "OUTBOX_POLL_INTERVAL", time.Second, &problems),
		OutboxBatchSize:    readInt(lookup, "OUTBOX_BATCH_SIZE", 20, 1, 1_000, &problems),
		OutboxMaxAttempts:  readInt(lookup, "OUTBOX_MAX_ATTEMPTS", 10, 1, 1_000, &problems),
	}

	initialCredit := int64(readInt(lookup, "INITIAL_CREDIT_AMOUNT", 0, 0, MaxInitialCredit, &problems))
	if _, found := lookup("INITIAL_CREDIT_AMOUNT"); !found {
		problems = append(problems, "INITIAL_CREDIT_AMOUNT is required (use 0 for empty wallets)")
	}

	authConfig := auth.DefaultAuth0Config()
	authConfig.Issuer = strings.TrimSpace(required(lookup, "AUTH0_ISSUER", &problems))
	authConfig.Audience = read(lookup, "AUTH0_AUDIENCE", authConfig.Audience)
	skew, err := time.ParseDuration(read(lookup, "AUTH0_CLOCK_SKEW", authConfig.ClockSkew.String()))
	if err != nil {
		problems = append(problems, "AUTH0_CLOCK_SKEW must be a duration")
	} else {
		authConfig.ClockSkew = skew
	}
	authConfig.CacheTTL = readDuration(lookup, "AUTH0_JWKS_CACHE_TTL", authConfig.CacheTTL, &problems)
	authConfig.FetchTimeout = readDuration(lookup, "AUTH0_JWKS_FETCH_TIMEOUT", authConfig.FetchTimeout, &problems)
	authConfig.RefreshInterval = readDuration(lookup, "AUTH0_JWKS_REFRESH_INTERVAL", authConfig.RefreshInterval, &problems)
	authConfig.FetchAttempts = readInt(lookup, "AUTH0_JWKS_FETCH_ATTEMPTS", authConfig.FetchAttempts, 1, 3, &problems)
	authConfig.BreakerThreshold = readInt(lookup, "AUTH0_JWKS_BREAKER_THRESHOLD", authConfig.BreakerThreshold, 1, 100, &problems)
	authConfig.BreakerCooldown = readDuration(lookup, "AUTH0_JWKS_BREAKER_COOLDOWN", authConfig.BreakerCooldown, &problems)
	if err := authConfig.Validate(); err != nil {
		problems = append(problems, err.Error())
	}

	if len(problems) > 0 {
		return Config{}, &Error{problems: problems}
	}

	return Config{
		Auth:          authConfig,
		InitialCredit: initialCredit,
		Environment:   environment,
		HTTP:          HTTPConfig{Host: host, Port: port},
		Database: DatabaseConfig{
			URL:            databaseURL,
			MinConnections: int32(poolMin),
			MaxConnections: int32(poolMax),
		},
		Messaging:       messaging,
		StartupTimeout:  startupTimeout,
		LogLevel:        logLevel,
		ShutdownTimeout: shutdownTimeout,
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

func readInt(lookup LookupEnv, key string, fallback, minimum, maximum int, problems *[]string) int {
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

func readDuration(lookup LookupEnv, key string, fallback time.Duration, problems *[]string) time.Duration {
	value := read(lookup, key, fallback.String())
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		*problems = append(*problems, key+" must be a positive duration")
		return fallback
	}
	return parsed
}

func validLogLevel(level string) bool {
	switch level {
	case "debug", "info", "warn", "error":
		return true
	default:
		return false
	}
}

func validPostgreSQLURL(value string) bool {
	parsed, err := url.ParseRequestURI(value)
	if err != nil {
		return false
	}
	return (parsed.Scheme == "postgres" || parsed.Scheme == "postgresql") && parsed.Host != ""
}

func validAMQPURL(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil {
		return false
	}
	return (parsed.Scheme == "amqp" || parsed.Scheme == "amqps") && parsed.Host != ""
}
