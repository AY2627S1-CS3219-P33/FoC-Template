package dashboardapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/servicehealth"
)

// Readiness reads every service's /readyz; *servicehealth.Checker implements
// it.
type Readiness interface {
	Check(ctx context.Context) servicehealth.Report
}

var _ Readiness = (*servicehealth.Checker)(nil)

// ReadinessHandler serves GET /v1/readiness.
type ReadinessHandler struct {
	readiness Readiness
	now       Clock
	guard     func(http.Handler) http.Handler
	logger    *slog.Logger
}

func NewReadinessHandler(readiness Readiness, now Clock, guard *auth.Middleware, logger *slog.Logger) ReadinessHandler {
	switch {
	case readiness == nil:
		panic("dashboardapi: readiness is required")
	case now == nil:
		panic("dashboardapi: clock is required")
	case guard == nil:
		panic("dashboardapi: auth middleware is required")
	case logger == nil:
		panic("dashboardapi: logger is required")
	}
	return ReadinessHandler{readiness: readiness, now: now, guard: guard.RequireDashboard, logger: logger}
}

// RegisterRoutes registers the readiness route behind
// auth.Middleware.RequireDashboard.
func (h ReadinessHandler) RegisterRoutes(router *http.ServeMux) {
	router.Handle("GET /v1/readiness", h.guard(http.HandlerFunc(h.get)))
}

type readinessResponse struct {
	AsOf time.Time `json:"asOf"`
	// Services is every service that exposes /readyz, in a fixed order.
	Services []serviceReadiness `json:"services"`
	// NotChecked lists services the dashboard cannot check, and why.
	NotChecked []notCheckedService `json:"notChecked"`
}

type serviceReadiness struct {
	Name string `json:"name"`
	// State is "up" (answered 200: ready or degraded) or "down".
	State string `json:"state"`
	// Status is the service's own word, "ready" or "degraded"; omitted when down.
	Status string `json:"status,omitempty"`
	// Dependencies is the detail the service reports; omitted when it reports none.
	Dependencies map[string]string `json:"dependencies,omitempty"`
	// Reason says why a service is down.
	Reason        string `json:"reason,omitempty"`
	LatencyMillis int64  `json:"latencyMillis"`
}

type notCheckedService struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

func (h ReadinessHandler) get(response http.ResponseWriter, request *http.Request) {
	asOf := h.now()
	report := h.readiness.Check(request.Context())
	body := readinessResponse{
		AsOf:       asOf.UTC(),
		Services:   make([]serviceReadiness, 0, len(report.Services)),
		NotChecked: make([]notCheckedService, 0, len(report.NotChecked)),
	}
	for _, s := range report.Services {
		body.Services = append(body.Services, serviceReadiness{
			Name: s.Name, State: string(s.State), Status: s.Status, Dependencies: s.Dependencies,
			Reason: string(s.Reason), LatencyMillis: s.LatencyMillis,
		})
	}
	for _, n := range report.NotChecked {
		body.NotChecked = append(body.NotChecked, notCheckedService{Name: n.Name, Reason: n.Reason})
	}
	writeJSON(response, http.StatusOK, body)
}
