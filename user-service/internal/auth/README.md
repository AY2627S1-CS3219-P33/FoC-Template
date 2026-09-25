# Auth0 identity integration

`UserInfoClient` retrieves the profile associated with a validated Auth0 access token without exposing the token or upstream response body in errors. Auth0 Universal Login owns registration, credentials, password policy, password reset, login, logout, and sessions. This service stores no passwords or local sessions.

- Validate access tokens in middleware before trusting their subject.
- Match `/userinfo.sub` to the validated token subject before provisioning.
- Require a verified email on the exact `u.nus.edu` domain.
- Use Auth0 APIs for future re-authentication and account-wide session revocation.
- Keep tokens and Auth0 diagnostics free of credentials and personal data.

Account activity checks and role decisions require service/middleware coordination; a valid credential or token alone must not override account deactivation.
