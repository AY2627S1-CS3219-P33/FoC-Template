package serviceauth

import (
	"errors"
	"log/slog"
	"net/url"
	"strings"
)

// Config identifies this service to Auth0 for the client-credentials grant.
// The caller fills it; this package never reads the environment.
type Config struct {
	// Issuer is the Auth0 tenant origin with a trailing slash, for example
	// "https://tenant.auth0.com/". Tokens are requested from {Issuer}oauth/token.
	Issuer       string
	ClientID     string
	ClientSecret string
	// Audience is the API identifier of the service being called.
	Audience string
}

// Validate deliberately excludes configuration values from errors.
func (c Config) Validate() error {
	u, err := url.Parse(c.Issuer)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		u.Path != "/" || u.RawPath != "" {
		return errors.New("serviceauth: issuer must be an HTTPS origin with a trailing slash")
	}
	if strings.TrimSpace(c.ClientID) == "" {
		return errors.New("serviceauth: client ID is required")
	}
	if c.ClientSecret == "" {
		return errors.New("serviceauth: client secret is required")
	}
	if c.Audience == "" || strings.TrimSpace(c.Audience) != c.Audience ||
		strings.ContainsAny(c.Audience, " \t\r\n,") {
		return errors.New("serviceauth: audience must be a nonempty API identifier")
	}
	return nil
}

// String redacts the client secret so that formatting a Config with %v, %+v
// or %s cannot leak it.
func (c Config) String() string {
	return "serviceauth.Config{Issuer:" + c.Issuer + " ClientID:" + c.ClientID +
		" ClientSecret:" + redacted(c.ClientSecret) + " Audience:" + c.Audience + "}"
}

// GoString redacts the client secret for %#v.
func (c Config) GoString() string { return c.String() }

// LogValue redacts the client secret when a Config is passed to slog.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("issuer", c.Issuer),
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
