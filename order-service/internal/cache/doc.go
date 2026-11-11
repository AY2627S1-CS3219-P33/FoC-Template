// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-10-18
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

// Package cache holds order-service's Redis read-cache adapters and their
// no-op counterparts (NFR5.1, NFR7.1).
//
// Redis is optional and is never a source of truth: an empty REDIS_URL
// selects the no-op adapters, and every Redis failure (unreachable server,
// per-command timeout, open breaker, undecodable value) is reported to the
// caller as a miss so the read falls through to Postgres or the upstream
// service. Failures are logged at debug level, rate limited, and never
// returned.
//
//   - Client wraps a go-redis client with a per-command timeout and a
//     resilience.Breaker, and implements KV.
//   - KV is a small JSON get/set helper used by the supplier caches (B2c).
//     Every key is stored under the "order:" namespace.
//   - RedisOpenErrandCache implements errand.OpenErrandCache under the key
//     "order:open-errands:v1". It stores only errand.OpenErrandView, the
//     redacted open-list projection, so no requester identity ever reaches
//     Redis (NFR3.6.4).
//   - NoopKV and NoopOpenErrandCache always miss.
package cache
