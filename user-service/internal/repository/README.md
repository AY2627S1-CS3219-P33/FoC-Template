# Persistence — planned responsibilities

Current implementation: `Postgres` uses pgx to provision active Auth0-backed USER accounts, find non-deleted accounts, update permitted profile fields, and soft-delete accounts. The account migration enforces unique email and Auth0 identities, including after deletion. `Open` connects and pings with redacted errors. Bootstrap persistence now locks its coordination row and commits account creation with durable completion atomically. Audit persistence remains unimplemented.

`SoftDelete` is a low-level primitive, not a complete deletion workflow: it neither checks other services nor invalidates sessions. The service layer deliberately does not call it yet.

| Requirements | Future persistence responsibility |
| --- | --- |
| F1.1.1, F1.9.1 | Enforce username, email, and external-identity uniqueness atomically, including concurrent requests. |
| F1.1, F1.1.4, F1.3.1 | Store the Auth0 subject, email, account role, and activation state; support account lookup. |
| F1.2.4, F1.3, F1.3.2, F1.10.4–F1.10.5 | Persist application account deletion/deactivation state; Auth0 owns session state and revocation. |
| F1.4.1–F1.4.3 | Read user-owned profile fields and persist permitted edits. Do not store an authoritative credit balance here. |
| F1.6.3, F1.6.6 | Support administrator account CRUD with authorization enforced by the service. |
| F1.1.4, F1.7, F1.9.3–F1.9.4 | Keep local application state synchronized with Auth0-managed verification, reset, and activation. |
| F1.6.7, F1.8.1, F1.8.4, F1.8.7 | Persist bootstrap completion and atomically coordinate initial account creation, ensuring no existing account is overwritten and no duplicate bootstrap occurs. |
| F1.10.1–F1.10.2, F1.10.7 | Provide transactions supporting deactivation/reactivation and preservation of at least one active super administrator under concurrency. |
| F1.9.5, F1.10.6 | Persist audit records for successful and failed attempts: actor, target, timestamp, and outcome. Do not persist credentials in audit data. |

Repositories query only the user service's database. Credit reservations, credit balance, active errands, courier participation, supplier records, and order state must be accessed through future service APIs/events, coordinated by the service layer.
