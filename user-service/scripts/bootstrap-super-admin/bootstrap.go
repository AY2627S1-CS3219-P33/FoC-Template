package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/joho/godotenv"
	"user-service/internal/config"
	"user-service/internal/repository"
	"user-service/internal/service"
)

// main runs administrator bootstrap and logs the outcome.
func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
	log.Print("super-administrator bootstrap complete")
}

// run loads deployment settings and bootstraps the initial administrator
// with a 30-second timeout.
func run() error {
	_ = godotenv.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := repository.Open(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer pool.Close()
	cfg := config.LoadBootstrap()
	return service.BootstrapSuperAdmin(ctx, repository.NewPostgres(pool), repository.CreateAuth0User{
		Subject: cfg.Subject, Username: cfg.Username, Email: cfg.Email,
	})
}
