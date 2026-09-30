# Authentication & Authorization Middleware

This document specifies the authentication and authorization cases handled by the HTTP middleware in [`middleware.go`](./middleware.go), adhering to the supplier service contracts, requirements F2.1–F2.4, and NFR3.3.

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
| **8. Insufficient Permissions** | Token is valid, but caller lacks required permission (e.g., normal user attempting admin write) | `403 Forbidden` | `FORBIDDEN` | `"insufficient permissions for this operation"` |
| **9. Ignored Client Spoofing** | Caller tries to pass `role=administrator` or `subject` in request queries or bodies | *N/A* | *N/A* | Ignored; identity and permissions are strictly derived from `auth.Principal` |
| **10. Successful Access** | Token is verified and permissions match (`principal.Has(permission) == true`) | *Pass-through* | *None* | Injects `auth.Principal` into context via `WithPrincipal` and calls `next.ServeHTTP(w, r.WithContext(ctx))` |

## Architectural Notes

1. **Decoupled Verification**: The middleware depends strictly on the [`Port`](./port.go) interface. The concrete identity-provider / token adapter can be plugged in without changing middleware logic.
2. **Context Propagation**: Downstream handlers retrieve the verified identity via [`auth.PrincipalFromContext(ctx)`](./context.go) rather than inspecting transport headers directly.
3. **Fail-Closed and Secure**: Unknown errors never leak credential strings or internal provider tracebacks across the HTTP boundary.
