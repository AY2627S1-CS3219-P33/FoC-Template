# Supplier Auth0 authentication (C1)

Supplier-service validates RS256 access tokens locally against Auth0 public
keys. A cache hit makes no Auth0 or user-service request. The caller's token
is never forwarded to user-service.

## Auth0 and local configuration

The supplier API Identifier is `https://api.foc.local/supplier-service`
(with no trailing comma). Enable RBAC and Add Permissions in the Access Token.
Define `suppliers:read` and `suppliers:manage`.

Defining a permission does not assign it to a user. Assign read permission to
active, provisioned users and both permissions to authorized administrators,
using Auth0 roles or direct permission assignments controlled by the account
owner. A role name alone does not grant supplier access.

Set these process environment variables (the executable does not load .env):

```text
AUTH0_ISSUER=https://<your-auth0-domain>/
AUTH0_AUDIENCE=https://api.foc.local/supplier-service
```

Use the exact issuer that appears in tokens, including the trailing slash.
When using an Auth0 custom domain, use that same domain for token acquisition
and verification. The existing user-service database/role is not consulted
by supplier-service. Supplier-service needs no client secret, signing private
key, or Auth0 Management API credential.

| Setting | Default | Bounds/behavior |
| --- | --- | --- |
| AUTH0_ISSUER | Required | HTTPS origin with trailing slash; no credentials, query, fragment, or path prefix |
| AUTH0_AUDIENCE | https://api.foc.local/supplier-service | Expected supplier API Identifier |
| AUTH0_CLOCK_SKEW | 30s | 0s through 1m |
| AUTH0_JWKS_CACHE_TTL | 5m | Positive, at most 1h; hard cache lifetime |
| AUTH0_JWKS_FETCH_TIMEOUT | 3s | Positive, at most 10s; total fetch/retry/wait deadline |
| AUTH0_JWKS_REFRESH_INTERVAL | 10s | Positive, no greater than cache TTL; minimum spacing of refreshes |
| AUTH0_JWKS_FETCH_ATTEMPTS | 1 | 1–3; retries only network failures, 429, or 5xx within the deadline |
| AUTH0_JWKS_BREAKER_THRESHOLD | 3 | 1–100 consecutive failed refresh sequences |
| AUTH0_JWKS_BREAKER_COOLDOWN | 30s | Positive, at most 1h |

## Client flow

The user-service login entry point may initiate Auth0 Universal Login. Auth0
authenticates the user and issues tokens; user-service provisions and manages
the local account. The browser uses Authorization Code with PKCE via Auth0's
SPA SDK.

Request a separate access token for each target API. For an already initialized
Auth0 SPA client, a supplier read request looks like:

```javascript
const token = await auth0Client.getTokenSilently({
  authorizationParams: {
    audience: "https://api.foc.local/supplier-service",
    scope: "suppliers:read"
  }
});

const response = await fetch("/suppliers", {
  headers: { Authorization: `Bearer ${token}` }
});
```

The example assumes the frontend's same-origin proxy routes /suppliers to
supplier-service. Cross-origin deployment needs its own approved CORS/proxy
configuration. Ask for both scopes for administrator workflows. Retain the
existing user-service audience for user-service requests. Configure Auth0
application access, callback URLs and allowed web origins for the frontend.

An existing Auth0 session may satisfy another audience request silently. Handle
login/consent-required errors by starting an interactive SDK authorization
request for the same supplier audience; do not substitute a user-service token.
A refresh token for one audience is not automatically valid for another.

Use access tokens, not ID tokens. Do not put tokens in URLs or logs. The
verifier checks the signature, RS256 algorithm, issuer, audience, expiry,
not-before (when present), and nonempty subject before trusting permissions.

## Permission and identity contract

`Principal.Subject` is Auth0's verified `sub` within the configured issuer,
not a local account UUID. `Principal.Permissions` comes from the verified
`permissions` array. `Roles` remains for source compatibility but grants no
permissions. Missing permissions, role-only principals, and unknown permission
names grant no supplier action.

All protected routes require `suppliers:read`; writes additionally require
`suppliers:manage`. Currently wired routes are list, current detail, immutable
version lookup, and create. Update and guarded delete remain feature work.

This is first-party user RBAC: the API checks assigned `permissions`, not
the requested/granted `scope` string. Adding third-party delegated clients
requires a policy that also constrains access to granted scopes.

| Outcome | HTTP/code |
| --- | --- |
| Missing, malformed, expired, bad-signature, wrong-issuer/audience token; unknown key after successful refresh | 401 UNAUTHENTICATED |
| Valid token without the operation's permissions | 403 FORBIDDEN |
| Required verification keys cannot be obtained | 503 DEPENDENCY_UNAVAILABLE |

## Key caching and operations

Keys come only from the configured issuer's /.well-known/jwks.json over
verified HTTPS. Redirects are disabled. Token-supplied issuer/key URLs never
choose the fetch destination. Auth0's validator/JWK libraries perform
cryptographic verification; a custom implementation of the provider's Cache
interface supplies the project's strict expiry, throttling and breaker policy.

Unknown key IDs can trigger a refresh once the refresh interval has elapsed.
Concurrent fetches coalesce; canceled waiters return promptly. A just-rotated
key may therefore be rejected until that interval elapses. Known keys remain
usable during an outage until the hard cache lifetime expires. Provider
Cache-Control cannot extend that lifetime. Failed refreshes never extend old
keys' validity. Invalid signatures do not trigger a refresh.

`auth.jwks_refresh` structured logs contain outcome, elapsed milliseconds and
circuit state, excluding tokens, key IDs, raw claims, provider bodies and URLs.
`Auth0.Stats()` exposes cache hits, fetches/failures, throttled requests and
circuit state for the platform's metrics integration. /readyz is public and
checks database/schema/seed readiness plus usable verification keys. A readiness
check can warm the cache; startup construction itself does not contact Auth0.

## Account lifecycle and remaining deployment gates

Auth0 permissions do not automatically mirror local user-service roles or
status. The account owner must provision/synchronize permissions and stop
new authorized token issuance/renewal after demotion, deactivation or deletion.
This change does not implement that cross-service synchronization or call the
Management API. Test both refresh-token and fresh-login paths before rollout.

Set the supplier API access-token lifetime to 600 seconds. Existing JWTs remain
valid until expiry (plus configured clock skew), even after logout or a
permission change. The total stale-authorization window includes the account
synchronization delay. This lifetime is tenant configuration, not inferred from
the user-service token settings. Confirm this window meets NFR3.3.2; immediate
account denial requires an additional authoritative check or reliable
revocation-state propagation.

The frontend API mapping and real tenant/account lifecycle acceptance remain
deployment work. No live Auth0 tokens or tenant settings are modified by these
repository changes.

## Verification

From supplier-service:

```sh
go test ./...
go vet ./...
```

Signed fixture tests use an isolated TLS JWKS server. They cover invalid
credentials, explicit permissions, key rotation/expiry, concurrent refreshes,
throttling, circuit recovery, TLS/redirect/timeout failures, redaction, and
composed list/detail/version/create routes without a live tenant or database.

For optional tenant acceptance, inject AUTH0_ISSUER and dedicated test values
AUTH0_SUPPLIER_READ_TOKEN, AUTH0_SUPPLIER_ADMIN_TOKEN, AUTH0_USER_SERVICE_TOKEN
through your environment. Set RUN_AUTH0_SMOKE_TEST=1 and run:

```sh
go test -count=1 -run '^TestAuth0TenantSmoke_C1$' ./tests
```

The test checks read-only and administrator permissions for the supplier
audience and rejection of the user-service token. Do not commit tokens or pass
them on command lines. It does not replace account-lifecycle acceptance.

References: [Auth0 RBAC](https://auth0.com/docs/get-started/apis/enable-role-based-access-control-for-apis),
[access tokens](https://auth0.com/docs/secure/tokens/access-tokens/get-access-tokens),
[JWKS](https://auth0.com/docs/secure/tokens/json-web-tokens/json-web-key-sets).
