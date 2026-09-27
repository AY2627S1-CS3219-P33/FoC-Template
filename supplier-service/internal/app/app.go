package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/config"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/database"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/httpapi"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/readiness"
)

type Application struct {
	database        *pgxpool.Pool
	server          *http.Server
	shutdownTimeout time.Duration
}

func New(ctx context.Context, cfg config.Config, logger *slog.Logger) (*Application, error) {
	pool, err := database.NewPool(ctx, cfg.Database)
	if err != nil {
		return nil, err
	}

	handler := httpapi.NewRouter(
		httpapi.Dependencies{Logger: logger},
		readiness.NewHandler(pool),
	)

	return &Application{
		database: pool,
		server: &http.Server{
			Addr:              cfg.HTTP.Address(),
			Handler:           handler,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      15 * time.Second,
			IdleTimeout:       60 * time.Second,
		},
		shutdownTimeout: cfg.ShutdownTimeout,
	}, nil
}

func (a *Application) Handler() http.Handler {
	return a.server.Handler
}

func (a *Application) Run(ctx context.Context) error {
	serverResult := make(chan error, 1)
	go func() {
		err := a.server.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serverResult <- err
	}()

	select {
	case err := <-serverResult:
		if err != nil {
			return fmt.Errorf("serve HTTP: %w", err)
		}
		return nil
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(
			context.Background(),
			a.shutdownTimeout,
		)
		defer cancel()

		if err := a.server.Shutdown(shutdownContext); err != nil {
			return errors.New("graceful HTTP shutdown failed")
		}

		if err := <-serverResult; err != nil {
			return fmt.Errorf("serve HTTP: %w", err)
		}
		return nil
	}
}

func (a *Application) Close() {
	a.database.Close()
}
