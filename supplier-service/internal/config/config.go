package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
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

type Config struct {
	Environment     Environment
	HTTP            HTTPConfig
	Database        DatabaseConfig
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

	if len(problems) > 0 {
		return Config{}, &Error{problems: problems}
	}

	return Config{
		Environment: environment,
		HTTP: HTTPConfig{
			Host: host,
			Port: port,
		},
		Database: DatabaseConfig{
			URL:            databaseURL,
			MinConnections: int32(poolMin),
			MaxConnections: int32(poolMax),
		},
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
