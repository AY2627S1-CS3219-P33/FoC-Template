package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"user-service/internal/auth"
	"user-service/internal/config"
	"user-service/internal/handler"
	authmiddleware "user-service/internal/middleware"
	"user-service/internal/repository"
	"user-service/internal/service"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	// Production supplies environment variables directly; .env is only a
	// local-development convenience.
	_ = godotenv.Load()

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	startupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := repository.Open(startupCtx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	authentication, err := authmiddleware.NewAuth0(cfg.Auth0Domain, cfg.Auth0Audience)
	if err != nil {
		return err
	}
	userInfo, err := auth.NewUserInfoClient(cfg.Auth0Domain, &http.Client{Timeout: 5 * time.Second})
	if err != nil {
		return err
	}
	users := repository.NewPostgres(pool)
	provisioner := service.NewAuth0Provisioner(users, userInfo)
	httpHandler := handler.New(handler.AuthConfig{
		Domain: cfg.Auth0Domain, ClientID: cfg.Auth0ClientID, Audience: cfg.Auth0Audience,
	}, authentication, provisioner)

	server := &http.Server{
		Addr:              cfg.HTTPAddress,
		Handler:           httpHandler.Router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serverErrors := make(chan error, 1)
	go func() {
		log.Printf("user-service listening on %s", cfg.HTTPAddress)
		serverErrors <- server.ListenAndServe()
	}()

	shutdownSignal, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case <-shutdownSignal.Done():
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		return server.Shutdown(shutdownCtx)
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
