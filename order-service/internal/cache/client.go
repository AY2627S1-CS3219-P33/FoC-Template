package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/resilience"
)

// KeyPrefix namespaces every key this package writes.
const KeyPrefix = "order:"

// Defaults for Config.
const (
	DefaultCommandTimeout = 50 * time.Millisecond
	DefaultLogInterval    = time.Minute
	DefaultBreakerName    = "redis"
)

// Config tunes a Client. The zero value is usable.
type Config struct {
	// CommandTimeout bounds every Redis command, including connecting.
	// Zero means DefaultCommandTimeout.
	CommandTimeout time.Duration
	// Breaker configures the circuit breaker in front of Redis. Zero fields
	// take the values of resilience.DefaultBreakerConfig(DefaultBreakerName).
	Breaker resilience.BreakerConfig
	// Logger receives the rate-limited debug logs. nil means slog.Default().
	Logger *slog.Logger
	// LogInterval is the minimum gap between two failure logs; failures in
	// between are counted and reported with the next log. Zero means
	// DefaultLogInterval.
	LogInterval time.Duration
	// Clock drives log rate limiting. nil means the wall clock. The breaker
	// takes its own clock from Breaker.Clock.
	Clock resilience.Clock
}

func (c Config) withDefaults() Config {
	if c.CommandTimeout <= 0 {
		c.CommandTimeout = DefaultCommandTimeout
	}
	defaults := resilience.DefaultBreakerConfig(DefaultBreakerName)
	if c.Breaker.Name == "" {
		c.Breaker.Name = defaults.Name
	}
	if c.Breaker.FailureThreshold == 0 {
		c.Breaker.FailureThreshold = defaults.FailureThreshold
	}
	if c.Breaker.CoolDown == 0 {
		c.Breaker.CoolDown = defaults.CoolDown
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.LogInterval <= 0 {
		c.LogInterval = DefaultLogInterval
	}
	if c.Clock == nil {
		c.Clock = wallClock{}
	}
	return c
}

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

// Client is a Redis client guarded by a per-command timeout and a circuit
// breaker. It implements KV. It is safe for concurrent use.
type Client struct {
	rdb     redis.UniversalClient
	breaker *resilience.Breaker
	timeout time.Duration
	log     *failureLog
}

var _ KV = (*Client)(nil)

// Open parses a redis:// or rediss:// URL and returns a Client for it. It
// does not contact Redis; use Ping for that. go-redis's own retries are
// disabled, since the caller always has a fallback and the breaker, not a
// retry loop, handles an unhealthy server.
func Open(redisURL string, cfg Config) (*Client, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		// The URL may carry a password, so neither it nor the parse error
		// (which can quote it) is included.
		return nil, errors.New("cache: invalid Redis URL")
	}
	cfg = cfg.withDefaults()
	opts.MaxRetries = -1
	opts.DialTimeout = cfg.CommandTimeout
	opts.ReadTimeout = cfg.CommandTimeout
	opts.WriteTimeout = cfg.CommandTimeout
	opts.ContextTimeoutEnabled = true
	rdb := redis.NewClient(opts)
	client, err := NewClient(rdb, cfg)
	if err != nil {
		_ = rdb.Close()
		return nil, err
	}
	return client, nil
}

// NewClient wraps an existing go-redis client. The Client takes ownership
// of rdb: Close closes it.
func NewClient(rdb redis.UniversalClient, cfg Config) (*Client, error) {
	if rdb == nil {
		return nil, errors.New("cache: Redis client is required")
	}
	cfg = cfg.withDefaults()
	breaker, err := resilience.NewBreaker(cfg.Breaker)
	if err != nil {
		return nil, fmt.Errorf("cache: %w", err)
	}
	return &Client{
		rdb:     rdb,
		breaker: breaker,
		timeout: cfg.CommandTimeout,
		log:     newFailureLog(cfg.Logger, cfg.Clock, cfg.LogInterval),
	}, nil
}

// Ping reports whether Redis answers, for readiness. Unlike the cache
// operations it returns the error. It goes through the breaker, so while the
// breaker is open it returns resilience.ErrCircuitOpen without contacting
// Redis, and a successful ping after the cool-down closes it again.
func (c *Client) Ping(ctx context.Context) error {
	return c.do(ctx, func(ctx context.Context) error {
		return c.rdb.Ping(ctx).Err()
	})
}

// Breaker returns the breaker in front of Redis, for readiness output.
func (c *Client) Breaker() *resilience.Breaker { return c.breaker }

// Close closes the underlying Redis client.
func (c *Client) Close() error { return c.rdb.Close() }

// GetJSON implements KV.
func (c *Client) GetJSON(ctx context.Context, key string, dst any) bool {
	key = KeyPrefix + key
	var data []byte
	found := false
	err := c.do(ctx, func(ctx context.Context) error {
		b, err := c.rdb.Get(ctx, key).Bytes()
		if errors.Is(err, redis.Nil) {
			return nil // a plain miss: Redis is healthy
		}
		data, found = b, err == nil
		return err
	})
	if err != nil {
		c.log.failure(ctx, "get", key, err)
		return false
	}
	if !found {
		return false
	}
	if err := json.Unmarshal(data, dst); err != nil {
		c.log.failure(ctx, "decode", key, err)
		return false
	}
	return true
}

// SetJSON implements KV.
func (c *Client) SetJSON(ctx context.Context, key string, value any, ttl time.Duration) {
	key = KeyPrefix + key
	if ttl < 0 {
		// go-redis reads a negative expiration as KEEPTTL, which would keep
		// an earlier expiry (or none) instead of the caller's intent.
		c.log.failure(ctx, "set", key, errors.New("negative TTL"))
		return
	}
	data, err := json.Marshal(value)
	if err != nil {
		c.log.failure(ctx, "encode", key, err)
		return
	}
	err = c.do(ctx, func(ctx context.Context) error {
		return c.rdb.Set(ctx, key, data, ttl).Err()
	})
	if err != nil {
		c.log.failure(ctx, "set", key, err)
	}
}

func (c *Client) do(ctx context.Context, fn func(context.Context) error) error {
	return resilience.Do(ctx, c.breaker, c.timeout, fn)
}
