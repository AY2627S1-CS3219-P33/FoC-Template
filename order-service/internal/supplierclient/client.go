package supplierclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/errand"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/resilience"
	"github.com/CS3219-AY2627S1/FoC-Template/order-service/internal/serviceauth"
)

// Defaults for Config.
const (
	DefaultTimeout     = 2 * time.Second
	DefaultBreakerName = "supplier"
	// maxBodyBytes bounds a response body. The largest response, 500
	// buildings, is far below it.
	maxBodyBytes = 1 << 20
)

// Config configures a Client.
type Config struct {
	// BaseURL is supplier-service's root, for example
	// "http://supplier-service:8080". Required.
	BaseURL string
	// Tokens supplies the service bearer token for supplier-service's
	// audience. Required.
	Tokens serviceauth.TokenSource
	// Timeout bounds each call, including reading the body. Zero means
	// DefaultTimeout.
	Timeout time.Duration
	// Breaker configures the supplier-service breaker. Zero fields take the
	// values of resilience.DefaultBreakerConfig(DefaultBreakerName).
	// IsFailure is always set by the Client.
	Breaker resilience.BreakerConfig
	// Transport is the base RoundTripper under the token transport. nil
	// means http.DefaultTransport.
	Transport http.RoundTripper
}

// Client is an errand.SupplierGate over supplier-service. It is safe for
// concurrent use.
type Client struct {
	base    string // no trailing slash
	http    *http.Client
	breaker *resilience.Breaker
	timeout time.Duration
}

var _ errand.SupplierGate = (*Client)(nil)

// New returns a Client. It does not contact supplier-service.
func New(cfg Config) (*Client, error) {
	if cfg.BaseURL == "" {
		return nil, errors.New("supplierclient: base URL is required")
	}
	base := strings.TrimRight(cfg.BaseURL, "/")
	if u, err := url.Parse(base); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" {
		return nil, errors.New("supplierclient: base URL must be an absolute http(s) URL")
	}
	if cfg.Tokens == nil {
		return nil, errors.New("supplierclient: token source is required")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	defaults := resilience.DefaultBreakerConfig(DefaultBreakerName)
	if cfg.Breaker.Name == "" {
		cfg.Breaker.Name = defaults.Name
	}
	if cfg.Breaker.FailureThreshold == 0 {
		cfg.Breaker.FailureThreshold = defaults.FailureThreshold
	}
	if cfg.Breaker.CoolDown == 0 {
		cfg.Breaker.CoolDown = defaults.CoolDown
	}
	cfg.Breaker.IsFailure = countsAgainstBreaker
	breaker, err := resilience.NewBreaker(cfg.Breaker)
	if err != nil {
		return nil, fmt.Errorf("supplierclient: %w", err)
	}
	return &Client{
		base: base,
		http: &http.Client{
			Transport: serviceauth.NewTransport(cfg.Transport, cfg.Tokens),
			// Internal calls never redirect; following one could send the
			// service token elsewhere.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		breaker: breaker,
		timeout: cfg.Timeout,
	}, nil
}

// Breaker returns the supplier-service breaker, for readiness output.
func (c *Client) Breaker() *resilience.Breaker { return c.breaker }

// ResolveForCreate implements errand.SupplierGate. It is never cached.
func (c *Client) ResolveForCreate(ctx context.Context, supplierID string, now time.Time) (errand.SupplierVersion, error) {
	if supplierID == "" {
		return errand.SupplierVersion{}, supplierUnavailable("supplier is unavailable")
	}
	var v errand.SupplierVersion
	err := c.get(ctx, "/suppliers/"+url.PathEscape(supplierID), nil, &v)
	if isNotFound(err) {
		return errand.SupplierVersion{}, supplierUnavailable("supplier is unavailable")
	}
	if err != nil {
		return errand.SupplierVersion{}, dependencyUnavailable()
	}
	if v.SupplierID != supplierID || v.VersionID == "" {
		return errand.SupplierVersion{}, dependencyUnavailable()
	}
	if !v.Available {
		return errand.SupplierVersion{}, supplierUnavailable("supplier is unavailable")
	}
	open, err := openAt(v.OpeningTime, v.ClosingTime, now)
	if err != nil {
		return errand.SupplierVersion{}, dependencyUnavailable()
	}
	if !open {
		return errand.SupplierVersion{}, supplierUnavailable("supplier is closed")
	}
	return v, nil
}

// GetVersion implements errand.SupplierGate. Versions of deleted suppliers
// stay readable, with Available false.
func (c *Client) GetVersion(ctx context.Context, versionID string) (errand.SupplierVersion, error) {
	if versionID == "" {
		return errand.SupplierVersion{}, supplierUnavailable("supplier version not found")
	}
	var v errand.SupplierVersion
	err := c.get(ctx, "/supplier-versions/"+url.PathEscape(versionID), nil, &v)
	if isNotFound(err) {
		return errand.SupplierVersion{}, supplierUnavailable("supplier version not found")
	}
	if err != nil || v.VersionID != versionID {
		return errand.SupplierVersion{}, dependencyUnavailable()
	}
	return v, nil
}

// ListBuildings implements errand.SupplierGate. Retired buildings are
// included so old delivery snapshots still resolve.
func (c *Client) ListBuildings(ctx context.Context) ([]errand.Building, error) {
	var body struct {
		Items []errand.Building `json:"items"`
	}
	query := url.Values{"includeRetired": {"true"}}
	if err := c.get(ctx, "/buildings", query, &body); err != nil {
		return nil, dependencyUnavailable()
	}
	if body.Items == nil {
		body.Items = []errand.Building{}
	}
	return body.Items, nil
}

// statusError is a non-200 answer from supplier-service.
type statusError struct{ status int }

func (e *statusError) Error() string {
	return fmt.Sprintf("supplier-service answered %d", e.status)
}

// isNotFound reports a 400 or 404: supplier-service is healthy and the ID
// names nothing it will serve.
func isNotFound(err error) bool {
	var se *statusError
	return errors.As(err, &se) && (se.status == http.StatusNotFound || se.status == http.StatusBadRequest)
}

// countsAgainstBreaker is the breaker's IsFailure: a not-found answer comes
// from a healthy dependency.
func countsAgainstBreaker(err error) bool { return !isNotFound(err) }

// get sends GET path?query through the breaker and decodes a 200 JSON body
// into dst. path must already be escaped. A non-200 answer is a
// *statusError.
func (c *Client) get(ctx context.Context, path string, query url.Values, dst any) error {
	target := c.base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	return resilience.Do(ctx, c.breaker, c.timeout, func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Accept", "application/json")
		resp, err := c.http.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			// Drain a little so the connection can be reused.
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			return &statusError{status: resp.StatusCode}
		}
		return json.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes)).Decode(dst)
	})
}

func supplierUnavailable(msg string) error {
	return &apperror.Error{Code: apperror.SupplierUnavailable, Message: msg}
}

func dependencyUnavailable() error {
	return &apperror.Error{Code: apperror.DependencyUnavailable, Message: "supplier-service is unavailable"}
}
