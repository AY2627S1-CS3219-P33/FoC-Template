# Migrations

`000001_accounts.up.sql` creates the user-service account table and case-insensitive unique username/email indexes. New accounts default to inactive USER; deleted accounts must remain inactive. Unique indexes include soft-deleted rows so their identities cannot be reused. No session, token, audit, or bootstrap schema is included.

Use PostgreSQL 16 or newer and apply the migration once to a dedicated user-service database:

```sh
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations/000001_accounts.up.sql
```

There is no automatic runner or migration ledger. Files contain their own transaction, so do not wrap them in an additional transaction. A repeated up migration fails rather than hiding schema drift.

`000001_accounts.down.sql` drops the accounts table and all account data. Use it only for an intentional rollback after arranging any necessary backup; it is not a normal restart step:

```sh
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations/000001_accounts.down.sql
```

The integration suite exercises both directions exclusively in isolated test schemas. Full bootstrap requirements below remain future work.

## Planned requirement allocation

- **F1.1.1, F1.9.1:** Eventually provide database constraints for unique usernames and email addresses.
- **F1.1–F1.4, F1.6–F1.10:** Introduce user-owned persistence structures as the corresponding account, credential, profile, role, session, token, and audit workflows are implemented. This is an allocation, not a complete schema proposal.
- **F1.6.7, F1.8:** Support the automated startup migration/bootstrap process, including durable initialization state and concurrency guarantees needed by the repository.
- **F1.8.1, F1.8.4, F1.8.7:** Enable atomic one-time initial super-administrator creation without overwriting existing accounts or creating duplicates during concurrent startup.

Startup orchestration belongs in `cmd/api`; bootstrap decisions belong in `internal/service`; deployment credentials come from `internal/config`; password hashes come from `internal/auth`; transactions belong in `internal/repository`. Together these satisfy the startup migration requirement without hardcoding secrets or introducing a separate password-hashing path in SQL.

Do not define tables for other services' errands, credits, supplier records, or order state here.
