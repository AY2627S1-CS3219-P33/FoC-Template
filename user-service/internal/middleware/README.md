# HTTP access enforcement — planned responsibilities

Auth0 authentication validates RS256 access-token signatures, issuer, audience, and lifetime with `go-jwt-middleware/v3`. Protected handlers receive the trusted `sub` claim through request context. Raw bearer tokens are used only after validation and only to retrieve the matching Auth0 `/userinfo` profile.

- **F1.3.1–F1.3.2:** Resolve the eventual session mechanism and attach authenticated identity to requests; reject expired, revoked, or otherwise invalid sessions on protected routes.
- **F1.2, F1.4:** Require authentication for self-deletion and profile access. Services enforce ownership and permitted operations.
- **F1.6, F1.6.3–F1.6.6:** Restrict administrator-management routes to super administrators and apply least privilege and need-to-know access. Services also enforce business and data-level restrictions.
- **F1.9.2:** Reject additional super-administrator creation by users and administrators.
- **F1.10, F1.10.5, F1.10.7:** Restrict super-administrator management to authenticated super administrators and deny protected access to deactivated accounts, including when an old session is presented.
- **F1.9.5, F1.10.6:** Ensure denied creation/deactivation attempts can be recorded by the future audit workflow rather than disappearing at the middleware boundary.

Self-deactivation, last-active-super-administrator protection, and re-authentication confirmation (F1.10.1–F1.10.3) remain service rules. Other services enforce their own errand, supplier, and dispute permissions using future identity/API/event contracts.
