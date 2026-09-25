package config

import "os"

// Bootstrap values are trusted deployment input, validated only when creation is needed.
type Bootstrap struct {
	Subject  string `json:"-"`
	Username string `json:"-"`
	Email    string `json:"-"`
}

func LoadBootstrap() Bootstrap {
	return Bootstrap{
		Subject:  os.Getenv("BOOTSTRAP_AUTH0_SUBJECT"),
		Username: os.Getenv("BOOTSTRAP_USERNAME"),
		Email:    os.Getenv("BOOTSTRAP_EMAIL"),
	}
}
