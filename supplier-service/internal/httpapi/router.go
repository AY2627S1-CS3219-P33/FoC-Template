package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Dependencies struct {
	Logger   *slog.Logger
	Database *pgxpool.Pool
}

func NewRouter(dependencies Dependencies) http.Handler {
	if dependencies.Logger == nil {
		panic("httpapi: logger is required")
	}
	if dependencies.Database == nil {
		panic("httpapi: database pool is required")
	}

	router := http.NewServeMux()

	// Routes are added here only as their OpenAPI operations are implemented.
	return router
}
