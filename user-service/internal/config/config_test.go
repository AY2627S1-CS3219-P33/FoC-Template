package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	t.Setenv("AUTH0_DOMAIN", "dev-example.us.auth0.com")
	t.Setenv("AUTH0_CLIENT_ID", "client-id")
	t.Setenv("AUTH0_AUDIENCE", "https://api.foc.local/user-service")
	t.Setenv("DATABASE_URL", " ")
	if _, err := Load(); err == nil {
		t.Fatal("missing database configuration accepted")
	}
	t.Setenv("DATABASE_URL", "postgres://student:secret@localhost/users")
	t.Setenv("HTTP_ADDRESS", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddress != ":8080" || cfg.DatabaseURL == "" {
		t.Fatal("configuration not loaded")
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret") {
		t.Fatal("database credentials serialized")
	}
	t.Setenv("HTTP_ADDRESS", "127.0.0.1:9090")
	cfg, err = Load()
	if err != nil || cfg.HTTPAddress != "127.0.0.1:9090" {
		t.Fatal("HTTP address override not loaded")
	}
}

func TestLoadRequiresAuth0Configuration(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://student:secret@localhost/users")
	values := map[string]string{
		"AUTH0_DOMAIN": "dev-example.us.auth0.com", "AUTH0_CLIENT_ID": "client-id",
		"AUTH0_AUDIENCE": "https://api.foc.local/user-service",
	}
	for missing := range values {
		t.Run(missing, func(t *testing.T) {
			for name, value := range values {
				t.Setenv(name, value)
			}
			t.Setenv(missing, " ")
			if _, err := Load(); err == nil {
				t.Fatalf("missing %s accepted", missing)
			}
		})
	}
}
