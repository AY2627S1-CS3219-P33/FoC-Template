// Package config loads application configuration from the environment.
package config

import (
	"errors"
	"os"
	"strings"
)

// Config holds application settings. Extend it as integrations are implemented.
type Config struct {
	HTTPAddress          string
	DatabaseURL          string `json:"-"`
	Auth0Domain          string `json:"-"`
	Auth0ClientID        string `json:"-"`
	Auth0Audience        string `json:"-"`
	Auth0M2MClientID     string `json:"-"`
	Auth0M2MClientSecret string `json:"-"`
	Auth0UserRoleID      string `json:"-"`
	Auth0AdminRoleID     string `json:"-"`
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
	auth0M2MClientID := strings.TrimSpace(os.Getenv("AUTH0_M2M_CLIENT_ID"))
	auth0M2MClientSecret := strings.TrimSpace(os.Getenv("AUTH0_M2M_CLIENT_SECRET"))
	auth0UserRoleID := strings.TrimSpace(os.Getenv("AUTH0_USER_ROLE_ID"))
	auth0AdminRoleID := strings.TrimSpace(os.Getenv("AUTH0_ADMIN_ROLE_ID"))
	if (auth0M2MClientID == "") != (auth0M2MClientSecret == "") {
		return Config{}, errors.New("AUTH0_M2M_CLIENT_ID and AUTH0_M2M_CLIENT_SECRET must be configured together")
	}
	if (auth0UserRoleID == "") != (auth0AdminRoleID == "") {
		return Config{}, errors.New("AUTH0_USER_ROLE_ID and AUTH0_ADMIN_ROLE_ID must be configured together")
	}
	return Config{
		HTTPAddress: address, DatabaseURL: databaseURL,
		Auth0Domain: auth0Domain, Auth0ClientID: auth0ClientID, Auth0Audience: auth0Audience,
		Auth0M2MClientID: auth0M2MClientID, Auth0M2MClientSecret: auth0M2MClientSecret,
		Auth0UserRoleID: auth0UserRoleID, Auth0AdminRoleID: auth0AdminRoleID,
	}, nil
}
