# Business services — planned requirement breakdown

Current implementation: internal student registration validation/persistence and active-account profile reads/updates. Registration stays inactive; verification is not sent. Profile credit balance is unavailable without an adapter. Business deletion validates confirmation/account state, then returns a dependency-unavailable error without modifying data. The remaining workflows below are planned, not implemented. See the root README for exact limits and usage.

## Registration — F1.1

- **F1.1:** Register a student using username, NUS email, and password with account role USER.
- **F1.1.1:** Require a unique email; usernames are non-unique display labels.
- **F1.1.2:** Require at least eight password characters, including uppercase, lowercase, number, and symbol; use the auth package for secure hashing.
- **F1.1.3:** Require password and confirmation to match exactly before proceeding.
- **F1.1.4:** Coordinate verification-email delivery and email ownership verification before account activation.
- **F1.1.5:** Validate the parsed email domain against `nus.edu.sg`. Treat an allowed suffix as a domain boundary: `nus.edu.sg` and its subdomains qualify, while an unrelated domain such as `notnus.edu.sg` does not.

## Account deletion — F1.2

- **F1.2, F1.2.1:** Allow authenticated self-deletion only after explicit confirmation.
- **F1.2.2:** Block deletion if credits are reserved in active errands; obtain this state from the credit-owning service through an API/event contract.
- **F1.2.3:** Block deletion while participating as courier in an active errand; obtain participation state from the errand-owning service.
- **F1.2.4:** On successful deletion, terminate all active sessions and prevent subsequent login.

Before implementation, define cross-service coordination so a reservation or courier assignment cannot race with deletion. Do not assume that a single remote lookup makes deletion safe. Deletion representation and record-retention rules remain undecided.

## Authentication and sessions — F1.3

- **F1.3, F1.3.1:** Authenticate only matching credentials for an active account, establish a session, and terminate the current session on logout.
- **F1.3.2:** Support Remember me persistence across browser sessions until expiry or logout, using the auth/session contracts. Session lifetimes remain to be defined.

## Profiles — F1.4

- **F1.4, F1.4.1:** Allow the authenticated account holder to view username, email, account role, and credit balance. Retrieve balance through the credit service's API/event integration; it is not user-service-owned data.
- **F1.4.2:** Permit edits only to display name and mobile number.
- **F1.4.3:** Reject edits to account role, credit balance, and other system-managed information through profile updates.

## Requester and courier activities — F1.5

- **F1.5, F1.5.1:** Expose one account identity for both requester and courier activities. Do not create separate requester/courier roles or accounts. Creating and accepting errands belongs to the errand-owning service.

## Role-based access — F1.6

- **F1.6, F1.6.1:** USER supports requesting and delivering errands; the owning service enforces those business permissions.
- **F1.6.2:** Administrator privileges cover supplier management and order disputes; those services enforce their respective operations.
- **F1.6.3, F1.6.6:** Only super administrators may create, read, edit, or delete administrator accounts.
- **F1.6.4–F1.6.5:** Enforce need-to-know access and least privilege in service operations and data returned, with middleware providing request-level checks.
- **F1.6.7:** Coordinate initial super-administrator creation through the startup migration/bootstrap process described in F1.8.

## Password reset — F1.7

- **F1.7:** Coordinate password reset through an email confirmation link using auth and email contracts.
- **F1.7.1:** Send the reset link only to the email used to create the target account; never use a caller-supplied alternative destination.

## Initial super-administrator bootstrap — F1.8

- **F1.8, F1.8.2:** Use credentials supplied by deployment configuration during initial startup.
- **F1.8.1:** Create the initial account only if bootstrap has never completed and no super-administrator record exists. An inactive record still counts as an existing record.
- **F1.8.3:** Hash the initial password using the same secure mechanism as other accounts.
- **F1.8.4:** Never overwrite or reset an existing super administrator on later startups.
- **F1.8.5:** Fail initialization with a configuration error for missing or invalid required bootstrap values while no super administrator exists.
- **F1.8.6:** Never expose credential values in errors, logs, or audit output.
- **F1.8.7:** Use an atomic repository transaction and durable initialization state to create at most one initial super administrator across concurrent startup instances.

If initialization is already marked complete but no super-administrator record exists, F1.8.1 prohibits automatic recreation. An explicit recovery policy is still needed; do not silently reset bootstrap state.

## Additional super administrators — F1.9

- **F1.9, F1.9.2:** Allow only an authenticated super administrator to create additional super administrators; reject users and administrators.
- **F1.9.1:** Require a unique email; usernames are non-unique display labels.
- **F1.9.3:** Arrange a time-limited activation link to the new account's registered email.
- **F1.9.4:** Keep the account inactive until its holder completes activation and sets a password.
- **F1.9.5:** Audit every creation attempt, recording creator, new account, timestamp, and outcome. For attempts rejected before an account exists, define a safe attempted-target representation without inventing an account record.

## Super-administrator deactivation and reactivation — F1.10

- **F1.10:** Allow an authenticated super administrator to deactivate another super administrator.
- **F1.10.1:** Reject self-deactivation.
- **F1.10.2:** Reject deactivation that would leave no active super administrator. Enforce this atomically so concurrent requests cannot bypass the rule.
- **F1.10.3:** Require the actor to re-authenticate before confirming deactivation.
- **F1.10.4–F1.10.5:** Terminate all target sessions and prevent login and protected operations after deactivation.
- **F1.10.6:** Audit every deactivation attempt with actor, affected account, timestamp, and outcome, including rejected attempts.
- **F1.10.7:** Allow an authenticated super administrator to reactivate a deactivated super administrator. Keep reactivation distinct from pending first-time activation and password setup.

Middleware rejections of auditable attempts must also reach the audit-recording path. Audit persistence and failure behavior need to be designed so rejected attempts are not lost when an account transaction rolls back.
