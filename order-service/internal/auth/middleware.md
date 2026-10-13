# Authentication & Authorization Middleware

This document specifies the authentication and authorization cases handled by the HTTP middleware in [`middleware.go`](./middleware.go), adhering to F1.6, NFR3.3 and NFR3.5.3. It is adapted from supplier-service's middleware.

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
| **9. Insufficient Permissions** | User token is valid, but caller lacks required permission (e.g., normal user calling a review endpoint) | `403 Forbidden` | `FORBIDDEN` | `"insufficient permissions for this operation"` |
| **10. Ignored Client Spoofing** | Caller tries to pass `role=administrator` or `subject` in request queries or bodies | *N/A* | *N/A* | Ignored; identity and permissions are strictly derived from `auth.Principal` |
| **11. Successful Access** | User token is verified and permissions match (`principal.Has(permission) == true`) | *Pass-through* | *None* | Injects `auth.Principal` into context via `WithPrincipal` and calls `next.ServeHTTP(w, r.WithContext(ctx))` |

Status codes come from `apperror.HTTPStatus`, the single code → status mapping.

## Permissions and token kinds

| Permission | Granted to | Token kind | Used by |
| :--- | :--- | :--- | :--- |
| `errands:use` | users (F1.6.1) | user | `RequireUse`: errand endpoints for requesters and couriers |
| `errands:review` | administrators (F1.6.2) | user | `RequireReview`: review, event-log and settings endpoints |
| `orders:internal` | services that call internal routes | service | `internal/serviceauth` (fence, account-deletion check) |

These names and the audience `https://api.foc.local/order-service` are the current proposal for gate C3 and must be agreed with the Auth0 / user-service owner.

A service token is an Auth0 client-credentials token: its `gty` claim is `client-credentials` and its subject is `<client-id>@clients`. Every other verified token is a user token. `RequirePermission` accepts user tokens only. Internal routes use `Middleware.Authenticate` and then require `IsService()` and `InternalAccess`.

## Architectural Notes

1. **Decoupled Verification**: The middleware depends strictly on the [`Port`](./port.go) interface. The concrete identity-provider / token adapter can be plugged in without changing middleware logic.
2. **Context Propagation**: Downstream handlers retrieve the verified identity via [`auth.PrincipalFromContext(ctx)`](./context.go) rather than inspecting transport headers directly.
3. **Fail-Closed and Secure**: Unknown errors never leak credential strings or internal provider tracebacks across the HTTP boundary.

Authorization uses explicit token permissions. Principal roles grant no access.
Subject is the Auth0 sub, not a local account ID.
