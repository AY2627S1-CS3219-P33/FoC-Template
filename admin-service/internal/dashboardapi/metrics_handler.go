// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-09
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

package dashboardapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/facts"
	"github.com/CS3219-AY2627S1/FoC-Template/admin-service/internal/metrics"
)

// Metrics is what the metric routes need; *metrics.Service implements it.
type Metrics interface {
	Location() *time.Location
	ErrandsPerDay(ctx context.Context, p metrics.Period) ([]metrics.DayCount, error)
	TimeToAccept(ctx context.Context, p metrics.Period) (metrics.TimeToAccept, error)
	Outcomes(ctx context.Context, p metrics.Period) (metrics.Outcomes, error)
	TopSuppliers(ctx context.Context, p metrics.Period, limit int) (metrics.Ranking, error)
	TopBuildings(ctx context.Context, p metrics.Period, limit int) (metrics.Ranking, error)
	ActiveCouriers(ctx context.Context, p metrics.Period) (int, error)
	RewardVolume(ctx context.Context, p metrics.Period) ([]metrics.DayReward, error)
}

var _ Metrics = (*metrics.Service)(nil)

// Clock tells the time; the zero value of nothing: pass time.Now in
// production.
type Clock func() time.Time

// MetricsHandler serves the metric routes.
type MetricsHandler struct {
	metrics Metrics
	now     Clock
	guard   func(http.Handler) http.Handler
	logger  *slog.Logger
}

func NewMetricsHandler(m Metrics, now Clock, guard *auth.Middleware, logger *slog.Logger) MetricsHandler {
	switch {
	case m == nil:
		panic("dashboardapi: metrics is required")
	case now == nil:
		panic("dashboardapi: clock is required")
	case guard == nil:
		panic("dashboardapi: auth middleware is required")
	case logger == nil:
		panic("dashboardapi: logger is required")
	}
	return MetricsHandler{metrics: m, now: now, guard: guard.RequireDashboard, logger: logger}
}

// RegisterRoutes registers the metric routes; each passes through
// auth.Middleware.RequireDashboard first.
func (h MetricsHandler) RegisterRoutes(router *http.ServeMux) {
	router.Handle("GET /v1/metrics/errands-per-day", h.guard(http.HandlerFunc(h.errandsPerDay)))
	router.Handle("GET /v1/metrics/time-to-accept", h.guard(http.HandlerFunc(h.timeToAccept)))
	router.Handle("GET /v1/metrics/outcomes", h.guard(http.HandlerFunc(h.outcomes)))
	router.Handle("GET /v1/metrics/top-suppliers", h.guard(http.HandlerFunc(h.topSuppliers)))
	router.Handle("GET /v1/metrics/top-buildings", h.guard(http.HandlerFunc(h.topBuildings)))
	router.Handle("GET /v1/metrics/active-couriers", h.guard(http.HandlerFunc(h.activeCouriers)))
	router.Handle("GET /v1/metrics/reward-volume", h.guard(http.HandlerFunc(h.rewardVolume)))
}

// header is what every metric response starts with.
type header struct {
	AsOf     time.Time `json:"asOf"`
	Timezone string    `json:"timezone"`
	From     string    `json:"from"`
	To       string    `json:"to"`
}

func (h MetricsHandler) header(asOf time.Time, p metrics.Period) header {
	return header{AsOf: asOf.UTC(), Timezone: p.Location.String(), From: p.From, To: p.To}
}

// period reads from and to, answering 400 itself when they are invalid. asOf
// is taken first, so the period's default end is the same moment.
func (h MetricsHandler) period(response http.ResponseWriter, request *http.Request) (time.Time, metrics.Period, bool) {
	asOf := h.now()
	query := request.URL.Query()
	p, err := metrics.NewPeriod(query.Get("from"), query.Get("to"), asOf, h.metrics.Location())
	if err != nil {
		var periodErr *metrics.PeriodError
		if errors.As(err, &periodErr) {
			writeError(response, apperror.InvalidArgument, periodErr.Message)
		} else {
			h.fail(response, request, "period", err)
		}
		return time.Time{}, metrics.Period{}, false
	}
	return asOf, p, true
}

func (h MetricsHandler) limit(response http.ResponseWriter, request *http.Request) (int, bool) {
	value := request.URL.Query().Get("limit")
	if value == "" {
		return metrics.DefaultRankLimit, true
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit < 1 || limit > metrics.MaxRankLimit {
		writeError(response, apperror.InvalidArgument, "limit must be between 1 and "+strconv.Itoa(metrics.MaxRankLimit))
		return 0, false
	}
	return limit, true
}

func (h MetricsHandler) fail(response http.ResponseWriter, request *http.Request, operation string, err error) {
	h.logger.ErrorContext(request.Context(), "metrics request failed", slog.String("operation", operation), slog.Any("error", err))
	writeError(response, apperror.Internal, "an unexpected internal error occurred")
}

// ---------------------------------------------------------------------------

type errandsPerDayResponse struct {
	header
	Days []dayCount `json:"days"`
}

type dayCount struct {
	Date    string `json:"date"`
	Created int    `json:"created"`
}

func (h MetricsHandler) errandsPerDay(response http.ResponseWriter, request *http.Request) {
	asOf, p, ok := h.period(response, request)
	if !ok {
		return
	}
	days, err := h.metrics.ErrandsPerDay(request.Context(), p)
	if err != nil {
		h.fail(response, request, "errands per day", err)
		return
	}
	body := errandsPerDayResponse{header: h.header(asOf, p), Days: make([]dayCount, 0, len(days))}
	for _, d := range days {
		body.Days = append(body.Days, dayCount{Date: d.Date, Created: d.Created})
	}
	writeJSON(response, http.StatusOK, body)
}

type timeToAcceptResponse struct {
	header
	Accepted      int      `json:"accepted"`
	MedianSeconds *float64 `json:"medianSeconds"`
	P90Seconds    *float64 `json:"p90Seconds"`
}

func (h MetricsHandler) timeToAccept(response http.ResponseWriter, request *http.Request) {
	asOf, p, ok := h.period(response, request)
	if !ok {
		return
	}
	result, err := h.metrics.TimeToAccept(request.Context(), p)
	if err != nil {
		h.fail(response, request, "time to accept", err)
		return
	}
	writeJSON(response, http.StatusOK, timeToAcceptResponse{
		header: h.header(asOf, p), Accepted: result.Accepted, MedianSeconds: result.MedianSeconds, P90Seconds: result.P90Seconds,
	})
}

type outcomesResponse struct {
	header
	Finished int            `json:"finished"`
	Outcomes []outcomeShare `json:"outcomes"`
}

type outcomeShare struct {
	Status facts.Status `json:"status"`
	Count  int          `json:"count"`
	Rate   float64      `json:"rate"`
}

func (h MetricsHandler) outcomes(response http.ResponseWriter, request *http.Request) {
	asOf, p, ok := h.period(response, request)
	if !ok {
		return
	}
	result, err := h.metrics.Outcomes(request.Context(), p)
	if err != nil {
		h.fail(response, request, "outcomes", err)
		return
	}
	body := outcomesResponse{header: h.header(asOf, p), Finished: result.Finished, Outcomes: make([]outcomeShare, 0, len(result.Items))}
	for _, item := range result.Items {
		body.Outcomes = append(body.Outcomes, outcomeShare{Status: item.Status, Count: item.Count, Rate: item.Rate})
	}
	writeJSON(response, http.StatusOK, body)
}

type rankingResponse struct {
	header
	// Basis says what the order is by, so a client need not guess.
	Basis        string       `json:"basis"`
	Items        []rankedItem `json:"items"`
	Unattributed int          `json:"unattributed"`
}

type rankedItem struct {
	ID        string  `json:"id"`
	Name      *string `json:"name"`
	Finished  int     `json:"finished"`
	Completed int     `json:"completed"`
}

func (h MetricsHandler) topSuppliers(response http.ResponseWriter, request *http.Request) {
	h.ranking(response, request, "top suppliers", h.metrics.TopSuppliers)
}

func (h MetricsHandler) topBuildings(response http.ResponseWriter, request *http.Request) {
	h.ranking(response, request, "top buildings", h.metrics.TopBuildings)
}

func (h MetricsHandler) ranking(response http.ResponseWriter, request *http.Request, operation string,
	rank func(context.Context, metrics.Period, int) (metrics.Ranking, error)) {
	asOf, p, ok := h.period(response, request)
	if !ok {
		return
	}
	limit, ok := h.limit(response, request)
	if !ok {
		return
	}
	result, err := rank(request.Context(), p, limit)
	if err != nil {
		h.fail(response, request, operation, err)
		return
	}
	body := rankingResponse{header: h.header(asOf, p), Basis: "finished", Items: make([]rankedItem, 0, len(result.Items)), Unattributed: result.Unattributed}
	for _, item := range result.Items {
		body.Items = append(body.Items, rankedItem{ID: item.ID, Name: item.Name, Finished: item.Finished, Completed: item.Completed})
	}
	writeJSON(response, http.StatusOK, body)
}

type activeCouriersResponse struct {
	header
	Couriers int `json:"couriers"`
}

func (h MetricsHandler) activeCouriers(response http.ResponseWriter, request *http.Request) {
	asOf, p, ok := h.period(response, request)
	if !ok {
		return
	}
	n, err := h.metrics.ActiveCouriers(request.Context(), p)
	if err != nil {
		h.fail(response, request, "active couriers", err)
		return
	}
	writeJSON(response, http.StatusOK, activeCouriersResponse{header: h.header(asOf, p), Couriers: n})
}

type rewardVolumeResponse struct {
	header
	Total     int64       `json:"total"`
	Completed int         `json:"completed"`
	Days      []dayReward `json:"days"`
}

type dayReward struct {
	Date      string `json:"date"`
	Reward    int64  `json:"reward"`
	Completed int    `json:"completed"`
}

func (h MetricsHandler) rewardVolume(response http.ResponseWriter, request *http.Request) {
	asOf, p, ok := h.period(response, request)
	if !ok {
		return
	}
	days, err := h.metrics.RewardVolume(request.Context(), p)
	if err != nil {
		h.fail(response, request, "reward volume", err)
		return
	}
	body := rewardVolumeResponse{header: h.header(asOf, p), Days: make([]dayReward, 0, len(days))}
	for _, d := range days {
		body.Total += d.Reward
		body.Completed += d.Completed
		body.Days = append(body.Days, dayReward{Date: d.Date, Reward: d.Reward, Completed: d.Completed})
	}
	writeJSON(response, http.StatusOK, body)
}

// ---------------------------------------------------------------------------

func writeJSON(response http.ResponseWriter, status int, body any) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(body)
}

func writeError(response http.ResponseWriter, code apperror.Code, message string) {
	auth.WriteError(response, code, message)
}
