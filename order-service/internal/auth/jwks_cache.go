package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/auth0/go-jwt-middleware/v3/jwks"
	"github.com/lestrrat-go/jwx/v3/jwk"
)

var errKeysUnavailable = errors.New("verification keys unavailable")

type JWKSStats struct {
	CacheHits     uint64
	Fetches       uint64
	FetchFailures uint64
	Throttled     uint64
	CircuitOpen   bool
}

// keyCache supplies the Auth0 provider's Cache contract. The library default
// extends TTL from Cache-Control and lacks a kid-triggered bounded refresh;
// this policy keeps C1's hard expiry and outage/error semantics explicit.
type keyCache struct {
	mu         sync.Mutex
	cfg        Auth0Config
	client     *http.Client
	uri        string
	now        func() time.Time
	logger     *slog.Logger
	set        jwk.Set
	expires    time.Time
	nextFetch  time.Time
	openUntil  time.Time
	failures   int
	lastFailed bool
	inFlight   chan struct{}
	stats      JWKSStats
}

func (c *keyCache) statsSnapshot() JWKSStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	stats := c.stats
	stats.CircuitOpen = c.now().Before(c.openUntil)
	return stats
}

func (c *keyCache) Get(ctx context.Context, uri string) (jwks.KeySet, error) {
	if uri != c.uri {
		return nil, errKeysUnavailable
	}
	kid, _ := ctx.Value(keyIDContextKey{}).(string)
	for {
		if ctx.Err() != nil {
			return nil, errKeysUnavailable
		}
		c.mu.Lock()
		now := c.now()
		fresh := c.set != nil && now.Before(c.expires)
		found := kid == ""
		if fresh && kid != "" {
			_, found = c.set.LookupKeyID(kid)
		}
		if fresh && found {
			c.stats.CacheHits++
			set := c.set
			c.mu.Unlock()
			return set, nil
		}
		if pending := c.inFlight; pending != nil {
			c.mu.Unlock()
			select {
			case <-pending:
				continue
			case <-ctx.Done():
				return nil, errKeysUnavailable
			}
		}
		if now.Before(c.nextFetch) || now.Before(c.openUntil) {
			c.stats.Throttled++
			set, failed := c.set, c.lastFailed
			c.mu.Unlock()
			// The last successful fetch authoritatively lacked this key.
			// A fetch failure instead means we cannot establish its validity.
			if fresh && !failed {
				return set, nil
			}
			return nil, errKeysUnavailable
		}
		c.inFlight = make(chan struct{})
		c.mu.Unlock()

		start := time.Now()
		set, err := c.fetch(ctx)

		c.mu.Lock()
		now = c.now()
		c.nextFetch = now.Add(c.cfg.RefreshInterval)
		c.lastFailed = err != nil
		if err != nil {
			c.failures++
			if c.failures >= c.cfg.BreakerThreshold {
				c.openUntil = now.Add(c.cfg.BreakerCooldown)
			}
		} else {
			c.set, c.expires = set, now.Add(c.cfg.CacheTTL)
			c.failures = 0
			c.openUntil = time.Time{}
		}
		open := now.Before(c.openUntil)
		close(c.inFlight)
		c.inFlight = nil
		c.mu.Unlock()
		outcome := "success"
		if err != nil {
			outcome = "unavailable"
		}
		c.logger.InfoContext(ctx, "auth.jwks_refresh",
			"outcome", outcome, "duration_ms", time.Since(start).Milliseconds(), "circuit_open", open)
		if err != nil {
			return nil, errKeysUnavailable
		}
		return set, nil
	}
}

// All attempts share one deadline; only network errors, 429 and 5xx retry.
// Defaults to one attempt. Bodies, URLs, tokens and provider errors are never logged.
func (c *keyCache) fetch(ctx context.Context) (jwk.Set, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.FetchTimeout)
	defer cancel()
	for attempt := 0; attempt < c.cfg.FetchAttempts; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(time.Duration(attempt) * 100 * time.Millisecond)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return nil, errKeysUnavailable
			}
		}
		c.mu.Lock()
		c.stats.Fetches++
		c.mu.Unlock()
		set, retry, err := c.fetchOnce(ctx)
		if err == nil {
			return set, nil
		}
		c.mu.Lock()
		c.stats.FetchFailures++
		c.mu.Unlock()
		if !retry || ctx.Err() != nil {
			break
		}
	}
	return nil, errKeysUnavailable
}

func (c *keyCache) fetchOnce(ctx context.Context) (jwk.Set, bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.uri, nil)
	if err != nil {
		return nil, false, errKeysUnavailable
	}
	response, err := c.client.Do(request)
	if err != nil {
		return nil, true, errKeysUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, response.StatusCode == 429 || response.StatusCode >= 500, errKeysUnavailable
	}
	const maxBody = 1024 * 1024
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		return nil, false, errKeysUnavailable
	}
	set, err := jwk.Parse(body)
	if err != nil || set.Len() == 0 {
		return nil, false, errKeysUnavailable
	}
	return set, false, nil
}
