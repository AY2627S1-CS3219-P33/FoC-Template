// Package dashboardapi serves the admin dashboard's read-only routes under
// /v1/. Each needs a user token with dashboard:read (auth.Middleware.
// RequireDashboard) and answers with an asOf time.
//
// Metrics, from the event-fed read model (internal/metrics); the optional
// from and to are dates, YYYY-MM-DD, both included, in the dashboard's time
// zone, and default to the last 30 days:
//
//   - GET /v1/metrics/errands-per-day
//   - GET /v1/metrics/time-to-accept
//   - GET /v1/metrics/outcomes
//   - GET /v1/metrics/top-suppliers and /v1/metrics/top-buildings (limit)
//   - GET /v1/metrics/active-couriers
//   - GET /v1/metrics/reward-volume
//
// Readiness, read live from each service's /readyz:
//
//   - GET /v1/readiness
package dashboardapi
