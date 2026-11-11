<!--
AI Assistance Disclosure:
Tool: Claude Code (model: Claude Opus 5.5), date: 2026-10-02
Scope: Documentation writing and formatting.
Author review: Reviewed, validated, and tested by the authors.
-->

# Authentication & Authorization Middleware

This document specifies the authentication and authorization cases handled by the HTTP middleware in [`middleware.go`](./middleware.go), adhering to F1.6, NFR3.3 and NFR3.5.3. It is copied from order-service's middleware.

## Handled Cases

| Case | Trigger Condition | HTTP Status | Error Code (`apperror.Code`) | Response / Action |
| :--- | :--- | :---: | :---: | :--- |
| **1. Missing Header** | `Authorization` header is empty or missing | `401 Unauthorized` | `UNAUTHENTICATED` | `"authorization header is required"` |
| **2. Malformed Scheme** | Header does not use `Bearer <token>` (e.g., `Basic ...` or single word) | `401 Unauthorized` | `UNAUTHENTICATED` | `"authorization header must use Bearer scheme"` |
| **3. Empty Token** | `Bearer` prefix is provided but the token is empty/whitespace | `401 Unauthorized` | `UNAUTHENTICATED` | `"bearer token is empty"` |
| **4. Invalid / Expired Token** | Port returns `InvalidCredential` | `401 Unauthorized` | `UNAUTHENTICATED` | `"invalid or expired credentials"` |
| **5. Disabled Account** | Port returns `AccountDisabled` | `403 Forbidden` | `FORBIDDEN` | `"account is disabled"` |
| **6. Verifier Unavailable** | Port returns `VerifierUnavailable` (user service or identity provider is down) | `503 Service Unavailable` | `DEPENDENCY_UNAVAILABLE` | `"authentication service unavailable"` |
| **7. Unexpected Error** | Port returns an unclassified error | `500 Internal Server Error` | `INTERNAL` | `"an unexpected internal error occurred"` *(prevents secret/credential leakage)* |
| **8. Service Credential on User Route** | Token is valid but is not a user token (e.g. Auth0 `gty` is `client-credentials`), whatever permissions it carries | `403 Forbidden` | `FORBIDDEN` | `"a user credential is required for this operation"` |
| **9. Insufficient Permissions** | User token is valid, but caller lacks the required permission (e.g., a token without `chats:use`) | `403 Forbidden` | `FORBIDDEN` | `"insufficient permissions for this operation"` |
| **10. Ignored Client Spoofing** | Caller tries to pass `role=administrator` or `subject` in request queries or bodies | *N/A* | *N/A* | Ignored; identity and permissions are strictly derived from `auth.Principal` |
| **11. Successful Access** | User token is verified and permissions match (`principal.Has(permission) == true`) | *Pass-through* | *None* | Injects `auth.Principal` into context via `WithPrincipal` and calls `next.ServeHTTP(w, r.WithContext(ctx))` |

Status codes come from `apperror.HTTPStatus`, the single code → status mapping.

## Permissions and token kinds

| Permission | Granted to | Token kind | Used by |
| :--- | :--- | :--- | :--- |
| `chats:use` | users and administrators | user | `RequireUse`: participant routes (`/me/chats`, `/errands/{errandId}/chats`, `/chats/{roomId}/messages`); the WebSocket checks it in its `auth` frame |

It is the only permission. It is defined on the Auth0 API `https://api.foc.local/chat-service` (see the chat-service README).

A service token is an Auth0 client-credentials token: its `gty` claim is `client-credentials` and its subject is `<client-id>@clients`. Every other verified token is a user token. `RequirePermission` accepts user tokens only; chat-service has no routes for services.

## Architectural Notes

1. **Decoupled Verification**: The middleware depends strictly on the [`Port`](./port.go) interface. The concrete identity-provider / token adapter can be plugged in without changing middleware logic.
2. **Context Propagation**: Downstream handlers retrieve the verified identity via [`auth.PrincipalFromContext(ctx)`](./context.go) rather than inspecting transport headers directly.
3. **Fail-Closed and Secure**: Unknown errors never leak credential strings or internal provider tracebacks across the HTTP boundary.

Authorization uses explicit token permissions. Principal roles grant no access.
Subject is the Auth0 sub, not a local account ID.
