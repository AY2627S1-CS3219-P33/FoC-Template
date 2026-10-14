// Package resilience holds the shared failure-isolation plumbing (NFR7.1):
// exponential backoff with full jitter, a circuit breaker, and Do, which runs
// one call under a per-call timeout and records its outcome on a breaker.
//
// B uses it for HTTP clients and Redis; C uses it for broker reconnects and
// outbox retry scheduling.
//
// There is deliberately no generic retry loop. An in-process retry loop is
// lost when the process restarts, cannot be bounded across replicas and makes
// it easy to repeat an operation that is not safe to repeat. Durable retries
// go through the outbox instead (NFR7.3.1): each row records its attempt
// count and next-attempt time, the dispatcher uses Backoff to schedule the
// next attempt, and a row that exhausts its documented attempt limit stays
// visible with its failure code (NFR7.4.1). Retrying inside a request is only
// allowed in callers whose operation is explicitly idempotent, and such a
// caller writes its own small, bounded loop around Do.
package resilience
