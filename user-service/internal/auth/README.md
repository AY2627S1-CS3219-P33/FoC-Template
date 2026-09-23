# Authentication primitives — planned responsibilities

Current implementation: `Argon2Hasher` securely hashes local passwords. `UserInfoClient` retrieves the profile associated with a validated Auth0 user access token without exposing the token or upstream response body in errors. Auth0 owns browser login, logout, and external-user sessions; local re-authentication and account-wide revocation remain future work.

- **F1.1.2, F1.3.1, F1.8.3, F1.9.4:** Provide one secure password-hashing and verification mechanism shared by normal accounts, bootstrap accounts, and activated super administrators. Registration policy and password-confirmation matching belong to the service workflow.
- **F1.3.1–F1.3.2:** Provide session creation, resolution, expiration, current-session logout, and Remember me support. Coordinate browser transport with handlers and persisted state with repositories.
- **F1.2.4, F1.10.4–F1.10.5:** Support revocation of all sessions for deleted or deactivated accounts and rejection of revoked sessions.
- **F1.1.4, F1.7, F1.9.3–F1.9.4:** Provide future verification, reset, and activation token primitives. Activation links must be time-limited. Algorithms, storage, expiry, and consumption semantics remain design work; no JWT choice is implied.
- **F1.10.3:** Verify re-authentication evidence for the acting super administrator before deactivation confirmation; the service controls the workflow.
- **F1.8.6:** Keep credentials out of diagnostics and errors.

Account activity checks and role decisions require service/middleware coordination; a valid credential or token alone must not override account deactivation.
