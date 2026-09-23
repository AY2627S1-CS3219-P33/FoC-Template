# Email delivery — planned responsibilities

Documentation only; no messages are sent and no provider is configured.

- **F1.1.4:** Deliver registration verification links to the registered NUS email address. The service validates NUS eligibility (F1.1.5) and decides when verification activates an account.
- **F1.7, F1.7.1:** Deliver password-reset confirmation links only to the address used to create the target account, resolved by the service rather than chosen as an alternative destination by the requester.
- **F1.9.3:** Deliver time-limited activation links to additional super administrators' registered email addresses. The activation workflow requires the recipient to set a password before activation (F1.9.4).

This package owns provider integration and future message templates. Auth/service components own token creation, validation, expiration, and account-state transitions. Delivery success alone never proves email ownership or activates an account.
