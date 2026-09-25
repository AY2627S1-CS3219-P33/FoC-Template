package config

import (
	"encoding/json"
	"testing"
)

func TestLoadBootstrap(t *testing.T) {
	t.Setenv("BOOTSTRAP_AUTH0_SUBJECT", "auth0|admin")
	t.Setenv("BOOTSTRAP_USERNAME", "admin")
	t.Setenv("BOOTSTRAP_EMAIL", "admin@example.com")
	got := LoadBootstrap()
	if got.Subject != "auth0|admin" || got.Username != "admin" || got.Email != "admin@example.com" {
		t.Fatal("bootstrap environment not loaded")
	}
	encoded, err := json.Marshal(got)
	if err != nil || string(encoded) != "{}" {
		t.Fatal("bootstrap values serialized", err)
	}
}
