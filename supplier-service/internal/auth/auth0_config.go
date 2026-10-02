package auth

import (
	"errors"
	"net/url"
	"strings"
	"time"
)

const SupplierAudience = "https://api.foc.local/supplier-service"

// Auth0Config contains public configuration only. No signing or management secret is needed.
type Auth0Config struct {
	Issuer           string
	Audience         string
	ClockSkew        time.Duration
	CacheTTL         time.Duration
	FetchTimeout     time.Duration
	RefreshInterval  time.Duration
	FetchAttempts    int
	BreakerThreshold int
	BreakerCooldown  time.Duration
}

func DefaultAuth0Config() Auth0Config {
	return Auth0Config{
		Audience:         SupplierAudience,
		ClockSkew:        30 * time.Second,
		CacheTTL:         5 * time.Minute,
		FetchTimeout:     3 * time.Second,
		RefreshInterval:  10 * time.Second,
		FetchAttempts:    1,
		BreakerThreshold: 3,
		BreakerCooldown:  30 * time.Second,
	}
}

// Validate deliberately excludes configuration values from errors.
func (c Auth0Config) Validate() error {
	u, err := url.Parse(c.Issuer)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		u.Path != "/" || u.RawPath != "" {
		return errors.New("AUTH0_ISSUER must be an HTTPS origin with a trailing slash")
	}
	if c.Audience == "" || strings.TrimSpace(c.Audience) != c.Audience ||
		strings.ContainsAny(c.Audience, " \t\r\n,") {
		return errors.New("AUTH0_AUDIENCE must be a nonempty API identifier")
	}
	if c.ClockSkew < 0 || c.ClockSkew > time.Minute {
		return errors.New("AUTH0_CLOCK_SKEW must be between 0s and 1m")
	}
	if c.CacheTTL <= 0 || c.CacheTTL > time.Hour {
		return errors.New("AUTH0_JWKS_CACHE_TTL must be positive and at most 1h")
	}
	if c.FetchTimeout <= 0 || c.FetchTimeout > 10*time.Second {
		return errors.New("AUTH0_JWKS_FETCH_TIMEOUT must be positive and at most 10s")
	}
	if c.RefreshInterval <= 0 || c.RefreshInterval > c.CacheTTL {
		return errors.New("AUTH0_JWKS_REFRESH_INTERVAL must be positive and no greater than cache TTL")
	}
	if c.FetchAttempts < 1 || c.FetchAttempts > 3 {
		return errors.New("AUTH0_JWKS_FETCH_ATTEMPTS must be between 1 and 3")
	}
	if c.BreakerThreshold < 1 || c.BreakerThreshold > 100 {
		return errors.New("AUTH0_JWKS_BREAKER_THRESHOLD must be between 1 and 100")
	}
	if c.BreakerCooldown <= 0 || c.BreakerCooldown > time.Hour {
		return errors.New("AUTH0_JWKS_BREAKER_COOLDOWN must be positive and at most 1h")
	}
	return nil
}
