# Email delivery — planned responsibilities

Documentation only; no messages are sent and no provider is configured.

- **F1.1.4, F1.7:** Registration verification and password-reset email are owned by Auth0, not this package.
- **F1.9.3:** Deliver application-specific administrator invitations only if Auth0 invitations do not cover the eventual workflow.

This package owns provider integration and future message templates. Auth/service components own token creation, validation, expiration, and account-state transitions. Delivery success alone never proves email ownership or activates an account.
