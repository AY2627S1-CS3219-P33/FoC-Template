package main

import (
	"context"
	"encoding/json"
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
	"user-service/internal/devauth"
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
	startupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := repository.Open(startupCtx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	// DEV-ONLY: when DEV_FAKE_AUTH=1, an in-process mock issuer replaces the real
	// Auth0 tenant so the service can be signed into locally without an account.
	// See internal/devauth. This block must never run in a deployed environment.
	var mock *devauth.Mock
	authClient := &http.Client{Timeout: 5 * time.Second}
	if os.Getenv("DEV_FAKE_AUTH") == "1" {
		mock, err = devauth.New(cfg.Auth0Domain, cfg.Auth0Audience)
		if err != nil {
			return err
		}
		authClient = mock.Client()
		log.Print("DEV_FAKE_AUTH enabled: using in-process mock Auth0 issuer; GET /dev/token mints access tokens")
	}

	var authentication *authmiddleware.Auth0
	if mock != nil {
		authentication, err = authmiddleware.NewAuth0WithClient(cfg.Auth0Domain, cfg.Auth0Audience, authClient)
	} else {
		authentication, err = authmiddleware.NewAuth0(cfg.Auth0Domain, cfg.Auth0Audience)
	}
	if err != nil {
		return err
	}
	userInfo, err := auth.NewUserInfoClient(cfg.Auth0Domain, authClient)
	if err != nil {
		return err
	}
	users := repository.NewPostgres(pool)
	bootstrap := config.LoadBootstrap()
	if err := service.BootstrapSuperAdmin(startupCtx, users, repository.CreateAuth0User{
		Subject: bootstrap.Subject, Username: bootstrap.Username, Email: bootstrap.Email,
	}); err != nil {
		return err
	}

	userService := service.NewUserService(users, nil)
	provisioner := service.NewAuth0Provisioner(users, userInfo)
	httpHandler := handler.New(handler.AuthConfig{
		Domain: cfg.Auth0Domain, ClientID: cfg.Auth0ClientID, Audience: cfg.Auth0Audience,
		Dev: mock != nil,
	}, authentication, provisioner, userService)

	// DEV-ONLY: mint a signed access token for any subject. Registered only when
	// the mock issuer is active, so it does not exist in a normal deployment.
	if mock != nil {
		httpHandler.Router.HandleFunc("GET /dev/token", func(w http.ResponseWriter, r *http.Request) {
			sub := valueOr(r.URL.Query().Get("sub"), "auth0|dev-student")
			email := valueOr(r.URL.Query().Get("email"), "devstudent@u.nus.edu")
			nickname := valueOr(r.URL.Query().Get("nickname"), "devstudent")
			name := valueOr(r.URL.Query().Get("name"), "Dev Student")
			token, err := mock.Mint(sub, email, nickname, name, 10*time.Minute)
			if err != nil {
				http.Error(w, "mint failed", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": token, "token_type": "Bearer", "expires_in": 600,
				"sub": sub, "email": email,
			})
		})
	}

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

// valueOr returns fallback when value is empty. DEV-ONLY helper for /dev/token.
func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
