# Migrations

`000001` and `000002` preserve the historical transition from local passwords to Auth0 identities. `000003_auth0_only.up.sql` rejects any remaining password-authenticated rows, removes `password_hash`, and requires every account to have an Auth0 subject. Ordinary USER accounts are activated by provisioning after a verified `u.nus.edu` profile is returned by Auth0. Initial SUPER_ADMIN bootstrap instead uses a trusted deployment-supplied identity and permits any valid email domain.

Use PostgreSQL 16 or newer and apply the migration once to a dedicated user-service database:

```sh
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations/000001_accounts.up.sql
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations/000002_auth0_identities.up.sql
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations/000003_auth0_only.up.sql
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations/000004_bootstrap_status.up.sql
```

There is no automatic runner or migration ledger. Files contain their own transaction, so do not wrap them in an additional transaction. A repeated up migration fails rather than hiding schema drift.

Roll back in reverse order. `000003_auth0_only.down.sql` restores the nullable legacy column before `000002` is rolled back. `000002_auth0_identities.down.sql` refuses to run while Auth0 accounts exist. `000001_accounts.down.sql` drops the accounts table and all account data.

```sh
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations/000004_bootstrap_status.down.sql
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations/000003_auth0_only.down.sql
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations/000002_auth0_identities.down.sql
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations/000001_accounts.down.sql
```

The integration suite exercises both directions exclusively in isolated test schemas. The coordination migration supports the shared command/API bootstrap workflow.

## Planned requirement allocation

- **F1.1.1, F1.9.1:** Usernames and email addresses remain case-insensitively unique.
- **F1.1–F1.4, F1.6–F1.10:** Introduce user-owned persistence structures for application accounts, profiles, roles, and audit workflows. Auth0 owns credentials and sessions.
- **F1.6.7, F1.8:** Support the automated startup migration/bootstrap process, including durable initialization state and concurrency guarantees needed by the repository.
- **F1.8.1, F1.8.4, F1.8.7:** Enable atomic one-time initial super-administrator creation without overwriting existing accounts or creating duplicates during concurrent startup.

Startup orchestration belongs in `cmd/api`; bootstrap decisions belong in `internal/service`; Auth0 configuration comes from `internal/config`; transactions belong in `internal/repository`.

Do not define tables for other services' errands, credits, supplier records, or order state here.

`000004` creates and seeds the bootstrap coordination row. It was previously named `000002_bootstrap_status.up.sql`; if that file was already applied, do not rerun the renamed migration. Its down migration removes durable initialization history and is for explicit rollback only, never a bootstrap retry.
