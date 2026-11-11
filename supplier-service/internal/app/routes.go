// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-10-02
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

package app

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/httpapi"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/readiness"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier/create"
	deletefeature "github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier/delete"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier/imageupload"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier/lookup"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier/query"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier/update"
	"github.com/CS3219-AY2627S1/FoC-Template/supplier-service/internal/supplier/versioning"
)

// NewHandler composes the same protected feature routes for runtime and tests.
// The feature handlers additionally check their operation-specific permission.
func NewHandler(logger *slog.Logger, verifier auth.Port, checker readiness.Checker, reader supplier.Reader, writer supplier.Writer, lookups supplier.LookupStore, deletions supplier.DeletionStore, fence deletefeature.OrderDeletionFence, images imageupload.Handler) http.Handler {
	protected := httpapi.NewRouter(httpapi.Dependencies{Logger: logger},
		query.NewHandler(reader), versioning.NewHandler(reader), create.NewHandler(writer),
		update.NewHandler(writer), deletefeature.NewHandler(deletions, fence), lookup.NewHandler(lookups), images)
	return httpapi.NewRouter(httpapi.Dependencies{Logger: logger},
		readiness.NewHandler(checker),
		protectedRoutes{handler: auth.NewMiddleware(verifier).RequireRead(protected)})
}

type protectedRoutes struct{ handler http.Handler }

func (p protectedRoutes) RegisterRoutes(mux *http.ServeMux) {
	// A feature route is reachable only through a prefix listed here, and
	// every listed prefix passes through authentication first.
	for _, pattern := range []string{
		"/suppliers", "/suppliers/", "/supplier-versions/",
		"/buildings", "/buildings/", "/supplier-types", "/supplier-types/",
	} {
		mux.Handle(pattern, p.handler)
	}
}

type allReady []readiness.Checker

func (checks allReady) Check(ctx context.Context) error {
	for _, check := range checks {
		if err := check.Check(ctx); err != nil {
			return err
		}
	}
	return nil
}
