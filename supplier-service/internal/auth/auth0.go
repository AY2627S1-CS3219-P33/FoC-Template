package auth

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/auth0/go-jwt-middleware/v3/jwks"
	"github.com/auth0/go-jwt-middleware/v3/validator"
	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jws"
)

type permissionClaims struct {
	Permissions []Permission `json:"permissions"`
}

func (c *permissionClaims) Validate(context.Context) error {
	for _, permission := range c.Permissions {
		if strings.TrimSpace(string(permission)) == "" {
			return errors.New("invalid permission claim")
		}
	}
	return nil
}

type keyIDContextKey struct{}

// Auth0 verifies tokens locally; only JWKS cache misses contact the fixed issuer.
type Auth0 struct {
	validator *validator.Validator
	cache     *keyCache
	timeout   time.Duration
}

// NewAuth0 builds the production verifier without fetching keys or opening a connection.
type Auth0Option func(*http.Client)

// WithHTTPClient supplies a deployment-controlled transport (for example, custom CA roots).
// The verifier still enforces its own timeout and disables redirects.
func WithHTTPClient(client *http.Client) Auth0Option {
	return func(target *http.Client) { *target = *client }
}

func NewAuth0(cfg Auth0Config, logger *slog.Logger, options ...Auth0Option) (*Auth0, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	client := &http.Client{Transport: transport}
	for _, option := range options {
		option(client)
	}
	return newAuth0(cfg, client, time.Now, logger)
}

func newAuth0(cfg Auth0Config, client *http.Client, now func() time.Time, logger *slog.Logger) (*Auth0, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	// Copy the client so redirects/timeouts cannot be weakened by the caller.
	httpClient := *client
	httpClient.Timeout = cfg.FetchTimeout
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	issuer, _ := url.Parse(cfg.Issuer)
	keysURL := issuer.ResolveReference(&url.URL{Path: "/.well-known/jwks.json"})
	cache := &keyCache{cfg: cfg, client: &httpClient, uri: keysURL.String(), now: now, logger: logger}
	provider, err := jwks.NewCachingProvider(
		jwks.WithIssuerURL(issuer),
		jwks.WithCustomJWKSURI(keysURL),
		jwks.WithCache(cache),
	)
	if err != nil {
		return nil, errors.New("configure Auth0 key provider")
	}
	v, err := validator.New(
		validator.WithKeyFunc(provider.KeyFunc),
		validator.WithAlgorithm(validator.RS256),
		validator.WithIssuer(cfg.Issuer),
		validator.WithAudience(cfg.Audience),
		validator.WithAllowedClockSkew(cfg.ClockSkew),
		validator.WithCustomClaims(func() *permissionClaims { return &permissionClaims{} }),
		validator.WithRegisteredClaimsValidator(func(c validator.RegisteredClaims) error {
			if strings.TrimSpace(c.Subject) == "" || c.Expiry <= 0 {
				return errors.New("subject and expiry are required")
			}
			return nil
		}),
	)
	if err != nil {
		return nil, errors.New("configure Auth0 token validator")
	}
	return &Auth0{validator: v, cache: cache, timeout: cfg.FetchTimeout}, nil
}

func (a *Auth0) Authenticate(ctx context.Context, token string) (Principal, error) {
	invalid := &AuthenticationError{Kind: InvalidCredential}
	// Bound parsing work. Only compact signed access tokens are supported.
	if len(token) == 0 || len(token) > 16*1024 || strings.Count(token, ".") != 2 {
		return Principal{}, invalid
	}
	message, err := jws.Parse([]byte(token))
	if err != nil || len(message.Signatures()) != 1 {
		return Principal{}, invalid
	}
	header := message.Signatures()[0].ProtectedHeaders()
	algorithm, ok := header.Algorithm()
	if !ok || algorithm != jwa.RS256() {
		return Principal{}, invalid
	}
	kid, ok := header.KeyID()
	if !ok || kid == "" || len(kid) > 256 {
		return Principal{}, invalid
	}
	// The untrusted kid only selects from the configured issuer's keys. It is
	// never a URL, trusted identity, permission, or log field.
	ctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	ctx = context.WithValue(ctx, keyIDContextKey{}, kid)
	raw, err := a.validator.ValidateToken(ctx, token)
	if err != nil {
		if errors.Is(err, errKeysUnavailable) {
			return Principal{}, &AuthenticationError{Kind: VerifierUnavailable}
		}
		return Principal{}, invalid
	}
	claims, ok := raw.(*validator.ValidatedClaims)
	if !ok || claims.HasConfirmation() {
		return Principal{}, invalid
	}
	custom, ok := claims.CustomClaims.(*permissionClaims)
	if !ok {
		return Principal{}, invalid
	}
	return Principal{
		Subject:     claims.RegisteredClaims.Subject,
		Permissions: append([]Permission(nil), custom.Permissions...),
	}, nil
}

// Check participates in readiness, warming keys without a caller credential.
func (a *Auth0) Check(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	_, err := a.cache.Get(ctx, a.cache.uri)
	return err
}

// Stats is a safe, low-cardinality snapshot for operational metrics.
func (a *Auth0) Stats() JWKSStats { return a.cache.statsSnapshot() }
