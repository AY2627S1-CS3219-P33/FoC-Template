// Package readiness serves GET /readyz (NFR4.1.2), unauthenticated.
//
// The service is not ready (503 {"status":"not_ready"}) only when Postgres is
// unreachable: every errand operation needs it. Every other dependency is
// optional for serving requests, so it is reported but never makes the
// service unready: Redis is a read cache, the outbox holds commands and
// events while RabbitMQ is down, and the supplier and user clients sit behind
// circuit breakers. When Postgres is reachable the response is 200 with
// "ready", or "degraded" if any other dependency is unhealthy, and the status
// of each:
//
//	{"status":"degraded","dependencies":{"postgres":"ok","redis":"ok",
//	 "rabbitmq":"degraded","creditResults":"stopped","supplier":"closed",
//	 "user":"closed"}}
//
// Dependencies are named Checkers, so this package never imports the
// packages that own them: application wiring (I1) passes in, for example,
// the RabbitMQ ping and credit-result consumer status from Person C's
// packages. All checks run in parallel, each with its own short timeout.
// The response holds only the fixed Status values below; a check's error
// text, URLs and connection strings never reach it.
package readiness
