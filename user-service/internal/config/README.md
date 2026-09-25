# Configuration — planned responsibilities

Current implementation: `Load` reads required `DATABASE_URL`, `AUTH0_DOMAIN`, `AUTH0_CLIENT_ID`, and `AUTH0_AUDIENCE`, plus optional `HTTP_ADDRESS` (default `:8080`). The Auth0 domain is a hostname without a scheme; the audience is the custom API identifier. Repository connection setup validates connectivity without exposing credentials.

- **F1.8.2:** Read initial super-administrator credentials from the deployment environment. Exact variable names remain to be defined.
- **F1.8.5:** Support validation of required bootstrap values when initialization is needed. The service determines this from persisted initialization/account state; configuration alone cannot decide.
- **F1.8.6:** Ensure configuration errors and diagnostics never expose credential values.
- **F1.1.4–F1.1.5, F1.3.2, F1.7, F1.9.3:** Provide future email-delivery, link, session-expiration, and activation-expiration settings as needed. NUS domain eligibility remains a service rule.

Session durations, token lifetimes, provider details, and other deployment settings are not specified by these requirements and remain to be decided. Do not put credentials in source files, documentation, or image defaults.
