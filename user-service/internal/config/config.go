// Package config loads application configuration from the environment.
package config

import (
	"errors"
	"os"
	"strings"
)

// Config holds application settings. Extend it as integrations are implemented.
type Config struct {
	HTTPAddress   string
	DatabaseURL   string `json:"-"`
	Auth0Domain   string `json:"-"`
	Auth0ClientID string `json:"-"`
	Auth0Audience string `json:"-"`
}

// Load requires a database URL; repository.Open validates it and connectivity.
func Load() (Config, error) {
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}
	address := strings.TrimSpace(os.Getenv("HTTP_ADDRESS"))
	if address == "" {
		address = ":8080"
	}
	auth0Domain := strings.TrimSpace(os.Getenv("AUTH0_DOMAIN"))
	if auth0Domain == "" {
		return Config{}, errors.New("AUTH0_DOMAIN is required")
	}
	auth0ClientID := strings.TrimSpace(os.Getenv("AUTH0_CLIENT_ID"))
	if auth0ClientID == "" {
		return Config{}, errors.New("AUTH0_CLIENT_ID is required")
	}
	auth0Audience := strings.TrimSpace(os.Getenv("AUTH0_AUDIENCE"))
	if auth0Audience == "" {
		return Config{}, errors.New("AUTH0_AUDIENCE is required")
	}
	return Config{
		HTTPAddress: address, DatabaseURL: databaseURL,
		Auth0Domain: auth0Domain, Auth0ClientID: auth0ClientID, Auth0Audience: auth0Audience,
	}, nil
}
