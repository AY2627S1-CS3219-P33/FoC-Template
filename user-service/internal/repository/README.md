# Persistence — planned responsibilities

Current implementation: `Postgres` uses pgx to create inactive USER accounts, find non-deleted accounts, update active accounts' permitted profile fields, and soft-delete accounts. The account migration enforces unique identities, including after deletion. Password hashes are accepted only as persistence input and are never returned in account records. `Open` connects and pings with redacted errors. Session, token, bootstrap, and audit persistence remain unimplemented; the allocation below describes future work beyond account persistence.

`SoftDelete` is a low-level primitive, not a complete deletion workflow: it neither checks other services nor invalidates sessions. The service layer deliberately does not call it yet.

| Requirements | Future persistence responsibility |
| --- | --- |
| F1.1.1, F1.9.1 | Enforce email and external-identity uniqueness atomically, including concurrent requests. Usernames are non-unique display labels. |
| F1.1, F1.1.4, F1.3.1 | Store user identity, password hashes, account role, and activation state; support account lookup. |
| F1.2.4, F1.3, F1.3.2, F1.10.4–F1.10.5 | Persist sessions, expiration and revocation state, and account deletion/deactivation state for the eventual session design. |
| F1.4.1–F1.4.3 | Read user-owned profile fields and persist permitted edits. Do not store an authoritative credit balance here. |
| F1.6.3, F1.6.6 | Support administrator account CRUD with authorization enforced by the service. |
| F1.1.4, F1.7, F1.9.3–F1.9.4 | Support the eventual verification/reset/activation lifecycle and expiration state; token storage details remain to be designed with auth. |
| F1.6.7, F1.8.1, F1.8.4, F1.8.7 | Persist bootstrap completion and atomically coordinate initial account creation, ensuring no existing account is overwritten and no duplicate bootstrap occurs. |
| F1.10.1–F1.10.2, F1.10.7 | Provide transactions supporting deactivation/reactivation and preservation of at least one active super administrator under concurrency. |
| F1.9.5, F1.10.6 | Persist audit records for successful and failed attempts: actor, target, timestamp, and outcome. Do not persist credentials in audit data. |

Repositories query only the user service's database. Credit reservations, credit balance, active errands, courier participation, supplier records, and order state must be accessed through future service APIs/events, coordinated by the service layer.
