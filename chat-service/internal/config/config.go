// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-09-29
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

// Package config reads chat-service's environment. Every problem is
// collected and reported at once, and no error includes a configured value,
// because URLs may carry credentials.
package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/auth"
)

type Environment string

const (
	Development Environment = "development"
	Test        Environment = "test"
	Production  Environment = "production"
)

// MaxMessageLength is the database's limit on a message body
// (chat_messages_body_length). CHAT_MAX_MESSAGE_LENGTH may lower it, never
// raise it.
const MaxMessageLength = 2000

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

// AMQPConfig is the RabbitMQ connection. There is no broker-less mode: rooms
// are created from errand events.
type AMQPConfig struct {
	URL string
	// Prefetch bounds the events RabbitMQ sends ahead of acknowledgements.
	Prefetch int
}

// ChatConfig holds the chat rules that are configurable.
type ChatConfig struct {
	// CloseDelay is how long a room stays writable after its errand ends.
	CloseDelay       time.Duration
	MaxMessageLength int
	// SendRate (messages per second) and SendBurst size the token bucket each
	// connection gets for sending.
	SendRate  float64
	SendBurst int
}

type WebSocketConfig struct {
	// AllowedOrigins are host patterns (path.Match syntax, no scheme) of the
	// pages allowed to open a WebSocket.
	AllowedOrigins        []string
	AuthTimeout           time.Duration
	PingInterval          time.Duration
	MaxConnectionsPerUser int
}

type Config struct {
	Environment     Environment
	HTTP            HTTPConfig
	LogLevel        string
	StartupTimeout  time.Duration
	ShutdownTimeout time.Duration
	Database        DatabaseConfig
	Auth            auth.Auth0Config
	AMQP            AMQPConfig
	Chat            ChatConfig
	WebSocket       WebSocketConfig
	// RedisURL is optional; empty selects the in-process broadcaster.
	RedisURL string
}

type LookupEnv func(string) (string, bool)

type Error struct {
	problems []string
}

func (e *Error) Error() string {
	return "invalid environment configuration: " + strings.Join(e.problems, "; ")
}

// Problems returns each problem found, in the order they were found.
func (e *Error) Problems() []string {
	return append([]string(nil), e.problems...)
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
	port := readInt(lookup, "HTTP_PORT", 3006, 1, 65_535, &problems)
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
	databaseURL := strings.TrimSpace(required(lookup, databaseKey, &problems))
	if databaseURL != "" && !validURL(databaseURL, "postgres", "postgresql") {
		problems = append(problems, databaseKey+" must be a PostgreSQL URL")
	}
	poolMin := readInt(lookup, "DATABASE_POOL_MIN", 1, 0, 1_000, &problems)
	poolMax := readInt(lookup, "DATABASE_POOL_MAX", 10, 1, 1_000, &problems)
	if poolMin > poolMax {
		problems = append(problems, "DATABASE_POOL_MIN must not exceed DATABASE_POOL_MAX")
	}

	amqpURL := strings.TrimSpace(required(lookup, "AMQP_URL", &problems))
	if amqpURL != "" && !validURL(amqpURL, "amqp", "amqps") {
		problems = append(problems, "AMQP_URL must be an amqp:// or amqps:// URL")
	}
	prefetch := readInt(lookup, "AMQP_PREFETCH", 10, 1, 1_000, &problems)

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
		// Validate names the variable at fault and never includes its value.
		if err := authConfig.Validate(); err != nil {
			problems = append(problems, err.Error())
		}
	}

	closeDelay := readDuration(lookup, "CHAT_CLOSE_DELAY", 24*time.Hour, &problems)
	maxLength := readInt(lookup, "CHAT_MAX_MESSAGE_LENGTH", MaxMessageLength, 1, MaxMessageLength, &problems)
	sendRate := readFloat(lookup, "CHAT_SEND_RATE", 5, 0, 100, &problems)
	sendBurst := readInt(lookup, "CHAT_SEND_BURST", 10, 1, 100, &problems)

	origins := readOrigins(lookup, "WS_ALLOWED_ORIGINS", &problems)
	authTimeout := readDuration(lookup, "WS_AUTH_TIMEOUT", 5*time.Second, &problems)
	pingInterval := readDuration(lookup, "WS_PING_INTERVAL", 25*time.Second, &problems)
	// The ALB closes connections idle for 60 s; pings keep them busy.
	if pingInterval >= time.Minute {
		problems = append(problems, "WS_PING_INTERVAL must be shorter than 1m")
	}
	maxConnections := readInt(lookup, "WS_MAX_CONNECTIONS_PER_USER", 5, 1, 100, &problems)

	redisURL := strings.TrimSpace(read(lookup, "REDIS_URL", ""))
	if redisURL != "" && !validURL(redisURL, "redis", "rediss") {
		problems = append(problems, "REDIS_URL must be a redis:// or rediss:// URL, or empty")
	}

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
		AMQP: AMQPConfig{URL: amqpURL, Prefetch: prefetch},
		Chat: ChatConfig{
			CloseDelay:       closeDelay,
			MaxMessageLength: maxLength,
			SendRate:         sendRate,
			SendBurst:        sendBurst,
		},
		WebSocket: WebSocketConfig{
			AllowedOrigins:        origins,
			AuthTimeout:           authTimeout,
			PingInterval:          pingInterval,
			MaxConnectionsPerUser: maxConnections,
		},
		RedisURL: redisURL,
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
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < minimum || parsed > maximum {
		*problems = append(
			*problems,
			fmt.Sprintf("%s must be an integer between %d and %d", key, minimum, maximum),
		)
		return fallback
	}
	return parsed
}

// readFloat accepts values above exclusiveMinimum and up to maximum.
func readFloat(
	lookup LookupEnv,
	key string,
	fallback, exclusiveMinimum, maximum float64,
	problems *[]string,
) float64 {
	value := read(lookup, key, strconv.FormatFloat(fallback, 'f', -1, 64))
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || parsed <= exclusiveMinimum || parsed > maximum {
		*problems = append(
			*problems,
			fmt.Sprintf("%s must be a number above %g and at most %g", key, exclusiveMinimum, maximum),
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
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
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
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil || parsed < 0 {
		*problems = append(*problems, key+" must be a duration")
		return fallback
	}
	return parsed
}

// readOrigins reads a required comma-separated list of host patterns, the
// form websocket.AcceptOptions.OriginPatterns expects: "app.bestfoc.app" or
// "localhost:5173", never "https://app.bestfoc.app".
func readOrigins(lookup LookupEnv, key string, problems *[]string) []string {
	value := required(lookup, key, problems)
	if value == "" {
		return nil
	}
	var origins []string
	for _, entry := range strings.Split(value, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if strings.Contains(entry, "://") || strings.ContainsAny(entry, "/ ") {
			*problems = append(*problems, key+" must list host patterns without a scheme or path")
			return nil
		}
		if _, err := path.Match(entry, ""); err != nil {
			*problems = append(*problems, key+" contains a malformed pattern")
			return nil
		}
		origins = append(origins, entry)
	}
	if len(origins) == 0 {
		*problems = append(*problems, key+" is required")
	}
	return origins
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
