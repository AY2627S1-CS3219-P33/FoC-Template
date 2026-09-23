// Package config loads application configuration from the environment.
package config

import (
	"errors"
	"os"
	"strings"
)

// Config holds application settings. Extend it as integrations are implemented.
type Config struct {
	HTTPAddress string
	DatabaseURL string `json:"-"`
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
	return Config{HTTPAddress: address, DatabaseURL: databaseURL}, nil
}
