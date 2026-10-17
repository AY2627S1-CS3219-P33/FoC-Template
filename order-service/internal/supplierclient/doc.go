// Package supplierclient implements errand.SupplierGate against
// supplier-service (F2.5.1, F2.5.2, F2.4.3, F3.1.1, NFR3.5, NFR7.1).
//
//   - Client calls GET /suppliers/{id}, GET /supplier-versions/{versionId}
//     and GET /buildings?includeRetired=true. Every request carries a
//     service bearer token (serviceauth.NewTransport) and runs through
//     resilience.Do with a per-call timeout and the supplier-service
//     breaker.
//   - ResolveForCreate refuses a supplier that is missing, deleted or closed
//     at the given instant. Opening hours are "HH:MM" in Asia/Singapore and
//     cover [opening, closing); a closing time at or before the opening time
//     crosses midnight, so equal times mean open all day.
//   - CachedGate wraps a gate with a cache.KV: supplier versions are cached
//     with no expiry (they are immutable) and the building list for five
//     minutes. ResolveForCreate always goes to supplier-service, because
//     availability must be checked live. A cache failure is a miss.
//
// Error mapping: a 400 or 404 from supplier-service (unknown or deleted
// supplier, unknown version, malformed ID) is SUPPLIER_UNAVAILABLE and does
// not count against the breaker. Anything else that stops a usable answer
// (transport error, timeout, open breaker, 401/403, 5xx, undecodable or
// inconsistent body, token failure) is DEPENDENCY_UNAVAILABLE. Messages
// never include URLs or tokens.
package supplierclient
