// Package readiness serves GET /readyz, unauthenticated.
//
// The service is not ready (503 {"status":"not_ready"}) only when Postgres is
// unreachable: every metric is read from it. The event consumer is reported
// but never makes the service unready, because the facts already stored can
// still be served while the broker is down. When Postgres is reachable the
// response is 200 with "ready", or "degraded" if another dependency is
// unhealthy, and the status of each:
//
//	{"status":"ready","dependencies":{"postgres":"ok","events":"running"}}
//
// The response holds only the fixed Status values below; a check's error
// text, URLs and connection strings never reach it. This is the same shape
// as order-service's /readyz, which the dashboard's readiness section reads.
package readiness
