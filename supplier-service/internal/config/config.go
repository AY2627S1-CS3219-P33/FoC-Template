package config

import (
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/auth"
)

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

// OrderServiceConfig configures the order-service deletion fence client
// and the deletion reconciler. An empty URL disables deletion: requests then
// fail closed with DELETION_FENCE_UNAVAILABLE.
type OrderServiceConfig struct {
	URL string
	// ClientID and ClientSecret are supplier-service's Auth0 M2M
	// application, authorised for Audience with orders:internal only.
	ClientID          string
	ClientSecret      string
	Audience          string
	Timeout           time.Duration
	MaxAttempts       int
	ReconcileInterval time.Duration
}

func (c OrderServiceConfig) Enabled() bool { return c.URL != "" }

// String redacts the client secret so formatting a Config cannot leak it.
func (c OrderServiceConfig) String() string {
	return fmt.Sprintf("config.OrderServiceConfig{URL:%s ClientID:%s ClientSecret:%s Audience:%s Timeout:%s MaxAttempts:%d ReconcileInterval:%s}",
		c.URL, c.ClientID, redacted(c.ClientSecret), c.Audience, c.Timeout, c.MaxAttempts, c.ReconcileInterval)
}

// GoString redacts the client secret for %#v.
func (c OrderServiceConfig) GoString() string { return c.String() }

// LogValue redacts the client secret when the config is passed to slog.
func (c OrderServiceConfig) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("url", c.URL),
		slog.String("client_id", c.ClientID),
		slog.String("client_secret", redacted(c.ClientSecret)),
		slog.String("audience", c.Audience),
	)
}

func redacted(secret string) string {
	if secret == "" {
		return ""
	}
	return "[REDACTED]"
}

type Config struct {
	Auth            auth.Auth0Config
	OrderService    OrderServiceConfig
	Environment     Environment
	HTTP            HTTPConfig
	Database        DatabaseConfig
	SeedNamespace   string
	SeedCSVPath     string
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
	port := readInt(lookup, "HTTP_PORT", 3002, 1, 65_535, &problems)
	logLevel := read(lookup, "LOG_LEVEL", "info")
	if !validLogLevel(logLevel) {
		problems = append(problems, "LOG_LEVEL must be debug, info, warn, or error")
	}

	shutdownTimeout := readDuration(
		lookup,
		"SHUTDOWN_TIMEOUT",
		10*time.Second,
		&problems,
	)

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
	seedNamespace := strings.TrimSpace(read(lookup, "SUPPLIER_SEED_DATASET_NAMESPACE", "template-v1"))
	seedCSVPath := strings.TrimSpace(required(lookup, "SUPPLIER_SEED_CSV_PATH", &problems))
	startupTimeout := readDuration(lookup, "STARTUP_TIMEOUT", 30*time.Second, &problems)
	if utf8.RuneCountInString(seedNamespace) > 120 {
		problems = append(problems, "SUPPLIER_SEED_DATASET_NAMESPACE must not exceed 120 characters")
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
	orderService := readOrderService(lookup, environment, &problems)

	if len(problems) > 0 {
		return Config{}, &Error{problems: problems}
	}

	return Config{
		Auth:         authConfig,
		OrderService: orderService,
		Environment:  environment,
		HTTP: HTTPConfig{
			Host: host,
			Port: port,
		},
		Database: DatabaseConfig{
			URL:            databaseURL,
			MinConnections: int32(poolMin),
			MaxConnections: int32(poolMax),
		},
		SeedNamespace:   seedNamespace,
		SeedCSVPath:     seedCSVPath,
		StartupTimeout:  startupTimeout,
		LogLevel:        logLevel,
		ShutdownTimeout: shutdownTimeout,
	}, nil
}

// readOrderService reports problems by variable name only; URLs and
// credentials are never echoed.
func readOrderService(lookup LookupEnv, environment Environment, problems *[]string) OrderServiceConfig {
	cfg := OrderServiceConfig{
		URL:               strings.TrimSpace(read(lookup, "ORDER_SERVICE_URL", "")),
		Audience:          strings.TrimSpace(read(lookup, "ORDER_AUDIENCE", "https://api.foc.local/order-service")),
		Timeout:           readDuration(lookup, "ORDER_SERVICE_TIMEOUT", 2*time.Second, problems),
		MaxAttempts:       readInt(lookup, "ORDER_SERVICE_MAX_ATTEMPTS", 2, 1, 5, problems),
		ReconcileInterval: readDuration(lookup, "DELETION_RECONCILE_INTERVAL", 15*time.Second, problems),
	}
	if cfg.Timeout > 30*time.Second {
		*problems = append(*problems, "ORDER_SERVICE_TIMEOUT must not exceed 30s")
	}
	if !cfg.Enabled() {
		return cfg
	}
	if !validOrderServiceURL(cfg.URL, environment) {
		*problems = append(*problems, "ORDER_SERVICE_URL must be an http(s) URL without a trailing slash, query, or credentials, and https when APP_ENV=production")
	}
	cfg.ClientID = strings.TrimSpace(required(lookup, "SUPPLIER_M2M_CLIENT_ID", problems))
	cfg.ClientSecret = required(lookup, "SUPPLIER_M2M_CLIENT_SECRET", problems)
	if !validAudience(cfg.Audience) {
		*problems = append(*problems, "ORDER_AUDIENCE must be a nonempty API identifier")
	}
	return cfg
}

func validAudience(audience string) bool {
	return audience != "" && !strings.ContainsAny(audience, " \t\r\n,")
}

func validOrderServiceURL(value string, environment Environment) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" ||
		parsed.ForceQuery || parsed.Fragment != "" || strings.HasSuffix(value, "/") {
		return false
	}
	if environment == Production {
		return parsed.Scheme == "https"
	}
	return parsed.Scheme == "https" || parsed.Scheme == "http"
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
