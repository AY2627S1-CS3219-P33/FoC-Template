# ECS deployment notes

Updated 11 November 2026. ECS revisions, Service Connect deployments, and task security groups were rechecked on this date. Broker details, ALB/CloudFront routing, IAM policy version and GitHub deployment results were last rechecked on 10 November. Database migration state below uses the earlier verified/user-reported checks from that day; a fresh local RDS connection timed out during the documentation refresh. This is an operational record, not infrastructure as code or a complete account audit. Do not add passwords, secret values, or bearer tokens.

## Current deployment

- Account: `842761392141`; Region: `ap-southeast-1`.
- ECS cluster: `comfortable-squirrel-pxwxt5`.
- All seven services run on Fargate in private subnets, with public IP assignment disabled and ARM64 task architecture. Each has desired/running count 1 and a completed rollout at the 11 November inspection.
- The frontend is static content in S3, served through CloudFront. The old frontend ECS service was removed.
- Browser entry point: <https://app.bestfoc.app/>. The original distribution address is `d8d2ig90yjarp.cloudfront.net`; use the custom frontend domain for same-site Auth0 login.
- CloudFront forwards API requests to the public ALB, which forwards to private ECS task IPs. Tasks use the NAT gateway for outbound internet access, including Auth0.
- Amazon MQ provides RabbitMQ. The shared RDS PostgreSQL instance holds the service databases.

The [all-component deployment](https://github.com/AY2627S1-CS3219-P33/FoC-Template/actions/runs/42768001825), attempt 2, succeeded for all five backends and the frontend at source commit `37b25c8891ddcb53cae87b7aac1db0a8781dcb70` (PR #102). ECS stability/public checks passed and the frontend HTML hash was verified through CloudFront. The public auth configuration returns `login.bestfoc.app`. This does not establish authenticated business-flow correctness; embedded login, token renewal with third-party cookies blocked, and admin metrics/backfill still need end-to-end verification.

## VPC and NAT gateway

VPC: `vpc-0856f69e3b57e3ea4`, CIDR `172.31.0.0/16`.

| Availability Zone | Private subnet | CIDR |
|---|---|---|
| `ap-southeast-1a` | `subnet-0b399c33d4b961d8f` | `172.31.48.0/24` |
| `ap-southeast-1b` | `subnet-0f07e7283c1cb1303` | `172.31.49.0/24` |
| `ap-southeast-1c` | `subnet-0201f7b8f28efc2b4` | `172.31.50.0/24` |

All three use `foc-private-rt` (`rtb-04360eed1b9d7dc28`):

| Destination | Target | Purpose |
|---|---|---|
| `172.31.0.0/16` | `local` | Traffic inside the VPC, including RDS and MQ |
| `0.0.0.0/0` | `nat-055dc5eae694f3711` | Other IPv4 traffic, including outbound Auth0 HTTPS |

The public zonal NAT gateway is in a public subnet in `ap-southeast-1a`, using Elastic IP allocation `eipalloc-026a7f3dbbbdaaf87`. Its subnet retains a route to the Internet Gateway. Original public subnets remain useful for the ALB.

One shared NAT gateway reduces the gateway count but creates an Availability Zone 1a dependency and possible cross-AZ data charges. NAT and its public IPv4 address remain billable when ECS tasks are stopped.

| Purpose | Security group | Relevant access |
|---|---|---|
| ALB | `sg-0419b641a4c392cde` (`alb-public-sg`) | Public HTTP listener on port 80 |
| Application tasks | `sg-0badb0855aab50221` | Service ports from ALB; internal service access as configured |
| Admin-service client | `sg-098b424831646a677` (`admin-service-client-sg`) | Attached to admin tasks; user-service allows TCP 8080 from this group |
| RDS | `sg-035aaddc7336f2898` | PostgreSQL 5432 from application security group |
| RabbitMQ | `sg-014bd894dbdad3f72` (`foc-rabbitmq-sg`) | AMQPS 5671 from application security group |

The replacement broker has only `foc-rabbitmq-sg` attached at the latest inspection. Verify individual rules before editing. RDS development access is a security-group rule, not an ALB target-group change: rule `sgr-072e7fc65bcbfcde7` was added to `General-Service-Dev` (`sg-01acd9c288bf007a5`) for TCP 5432 from `117.20.154.126/32`, the public IP observed during setup. The existing `172.20.10.2/32` rule uses a private address and does not identify the laptop's public internet source. Existing rules were preserved. Refresh the public-IP rule when the developer's IP changes; do not allow PostgreSQL from all IPs. RDS also had `Credit-Service-Dev` (`sg-0c181d0e29495dbe1`) and the application/RDS groups attached. A later direct RDS check timed out; the cause was not re-diagnosed during this notes refresh.

## ECS services and images

Active ECS service revisions at the 11 November inspection:

| Service | ECS service name | Verified task revision | Container port |
|---|---|---|---|
| User | `user-service-task-service-tb6pn0x0` | `user-service-task:7` | 8080 |
| Supplier | `supplier-service-task-service-ot78zbdx` | `supplier-service-task:8` | 3002 |
| Credit | `credit-service` | `credit-service-task:5` | 3004 |
| Order | `order-service-task-service-177de1f2` | `order-service-task:4` | 3003 |
| Admin | `admin-service-task-service-vmoebao1` | `admin-service-task:3` | 3005 |
| Chat | `chat-service` | `chat-service-task:1` | 3006 |
| Notification | `notification-service` | `notification-service-task:1` | 3007 |

ECR registry: `842761392141.dkr.ecr.ap-southeast-1.amazonaws.com`.

| Repository | Verified image digest (`sha256:` prefix omitted) |
|---|---|
| `user-service` | `b2be2a7aa587c08bd349f4d3090de7f7df1a9d1e92962abfe6d922d4bfc80700` |
| `supplier-service` | `328877cf85dee786850c418c02d8f8e83cb2b8d6a03016fe8dc13138c35817d9` |
| `credit-service` | `329a0cf38e781236702d746e2c88ac05defc482166a2fe52c2d21108bac200da` |
| `order-service` | `2c77946309b85240b38b399ec5f56f64c167715518032a55e226aa1a6e06163e` |
| `admin-service` | `b2826301a8f1b4ccefac664f04552b06e04c8ed6c7c6de99576b94f7db1fc6e4` |
| `chat-service` | `31e04b172ead6bb307e6800bc7228b1175ce4572f94998cfbd20f91f34d25d9e` |
| `notification-service` | `d8d7148baadf3f37d890ab48619c116130a547955058e11722dc809a33f3f47c` |

Build for `linux/arm64`. Supplier-service requires the repository root as build context so the seed CSV is included at `/app/data/supplier-seed-data.csv`:

```sh
docker build --platform linux/arm64 \
  -f supplier-service/Dockerfile \
  -t supplier-service:local .
```

The other service images use their service directories as build contexts. Publishing an image does not deploy it: select the task definition revision and update the ECS service. User-service task revision `user-service-task:8` is registered with the new Management API audience variable but is not selected by its ECS service; the active revision remains `:7`.

### Service Connect

Service Connect deployments in namespace `foc-local` at the 11 November inspection:

| Service | Role | Internal address |
|---|---|---|
| User | Server | `http://user-service-sc:8080` |
| Supplier | Server | `http://supplier-service:3002` |
| Credit | Server | `http://credit-service:3004` |
| Order | Server | `http://order-service:3003` |
| Admin | Client only | Joins the namespace but publishes no endpoint |

Chat and notification currently have no Service Connect configuration. These names work inside participating ECS tasks, not browsers. The user endpoint uses `user-service-sc` because the namespace already contains a separate service named `user-service`. Admin uses `USER_SERVICE_URL=http://user-service-sc:8080`. Deploy a server endpoint before redeploying clients that need to discover it. When inspecting ECS API output, check `deployments[].serviceConnectConfiguration`; the service-level `serviceConnectConfiguration` field is null even for the active deployments.

Service Connect currently uses HTTP internally. Auth0 service permissions still apply; discovery does not replace authorization.

## ALB and CloudFront routing

ALB: `foc-alb`, DNS `foc-alb-998900674.ap-southeast-1.elb.amazonaws.com`.

Reuse the existing HTTP port 80 listener when adding services. Another listener on port 80 fails with “Listener port already exists.”

| Priority | Incoming path | Path handling | Target group / port | Health check |
|---|---|---|---|---|
| 10 | `/api/*`, `/health` | Preserve | `user-service-tg` / 8080 | `/health`, HTTP 204 |
| 20 | `/supplier-service/*` | Strip `/supplier-service` | `supplier-service-tg` / 3002 | `/readyz`, HTTP 200 |
| 30 | `/credit-service/*` | Strip `/credit-service` | `credit-service-tg` / 3004 | `/readyz`, HTTP 200 |
| 40 | `/order-service/*` | Strip `/order-service` | `order-service-tg` / 3003 | `/readyz`, HTTP 200 |
| 50 | `/chat-service/*` | Strip `/chat-service` | `chat-service-tg` / 3006 | `/readyz`, HTTP 200 |
| 60 | `/admin-service/*` | Strip `/admin-service` | `admin-service-tg` / 3005 | `/readyz`, HTTP 200 |
| 70 | `/notification-service/*` | Strip `/notification-service` | `notification-service-tg` / 3007 | `/readyz`, HTTP 200 |

Target groups use IP targets registered by ECS. Listener port 80 can forward to a different container port. The listener's default action is now verified as fixed HTTP 404 (`Not found`). Prefix stripping uses ALB URL-rewrite transforms; admin uses regex `^/admin-service/(.*)$` and replacement `/$1`.

All seven target groups currently have a **300-second deregistration delay**. The successful all-component run spent approximately 9–11 minutes per backend, including build, startup, health checks and draining; the sequential deployment stage took about 50 minutes. A quiet log after image push can mean the script is waiting for ECS stability. Inspect ECS events, target health and rollout state before cancelling. The job timeout is 60 minutes. Investigate a service with no event progress for 10–15 minutes, repeated task failures, unhealthy targets or rollback. No draining settings were changed.

CloudFront distribution: `E39SHVSD0FXUB9` (`d8d2ig90yjarp.cloudfront.net`), with alternate domain `app.bestfoc.app`.

| Behavior | Origin | Configuration |
|---|---|---|
| `/assets/*` | `frontend-s3`, origin path `/frontend` | Static asset caching |
| `/api/*` | `foc-alb-api` | All seven HTTP methods; caching disabled |
| `/supplier-service/*` | `foc-alb-api` | All seven HTTP methods; caching disabled |
| `/credit-service/*` | `foc-alb-api` | All seven HTTP methods; caching disabled |
| `/order-service/*` | `foc-alb-api` | All seven HTTP methods; caching disabled |
| `/admin-service/*` | `foc-alb-api` | All seven HTTP methods; caching disabled |
| `/chat-service/*` | `foc-alb-api` | All seven HTTP methods; caching disabled |
| `/notification-service/*` | `foc-alb-api` | All seven HTTP methods; caching disabled |
| Default | Original S3 origin, no origin path | Existing images; default root object `frontend/index.html` |

API behaviors use `AllViewerExceptHostHeader`. ALB rules perform the prefix rewrites. No distribution-wide SPA error mapping was configured; such mappings can hide API/image errors.

**SPA deep links (not yet applied).** The admin area is served from client-side routes under `/admin/*` (for example `/admin/reviews`). The default behavior has no SPA fallback, so a refresh or direct link to one of these paths will make S3 answer 403/404 instead of returning the app. Do not add a distribution-wide custom error response (it would mask API and image errors). Instead, attach a CloudFront Function to the **default behavior only** (viewer request) that rewrites any URI whose last segment has no file extension to `/frontend/index.html`. API behaviors are separate and unaffected, and images and `/assets/*` have extensions. Not yet created or tested; this needs a change in the AWS console because no infrastructure-as-code exists. The same problem applies to the older `/admin` path.

Browser-to-CloudFront HTTPS works on the distribution domain; the user also confirmed HTTPS on `app.bestfoc.app` after attaching the issued ACM certificate from `us-east-1`. CloudFront-to-ALB used HTTP port 80 at the last inspection; origin HTTPS remains unverified. The ALB HTTP listener was publicly reachable at the last inspection.

## S3 frontend and supplier images

Bucket: `foc-supplier-images`, Region `ap-southeast-1`. The frontend and images deliberately share the bucket and CloudFront distribution.

- Original images remain at root keys such as `ANNA.jpeg`.
- Administrator uploads use `suppliers/`.
- Frontend output uses `frontend/index.html` and `frontend/assets/`.
- The bucket remains private; CloudFront reads through Origin Access Control and its bucket policy.
- Supplier CSV paths remain relative. `SUPPLIER_IMAGE_BASE_URL` supplies the public base URL; changing the CSV alone does not update existing database rows.
- The image backfill was run against AWS and image viewing was confirmed. See `supplier-service/scripts/backfill_images.py` and the supplier README for preview/apply instructions. Enter the administrator token through the hidden prompt.
- Administrator image uploads were confirmed after the WAF change below. Supplier task role `foc-supplier-task-role` has scoped S3 upload permission for `suppliers/*`.
- Abandoned/orphaned upload cleanup remains deferred.

### WAF upload exception

CloudFront web ACL `CreatedByCloudFront-c92a3694` blocked uploads with Common Rule Set rule `SizeRestrictions_BODY`, producing a CloudFront HTML 403 before the application received the request.

Only `SizeRestrictions_BODY` was changed from **Block** to **Count**. Other managed rules remained enabled, and uploads then worked. This exception applies wherever that rule group applies, not only the upload endpoint. Application administrator authorization, image validation, and the 5 MiB application upload limit remain in place.

## RDS databases and migrations

Instance: `foc-shared-database`, PostgreSQL 18.3 as reported in the console.

Endpoint: `foc-shared-database.cn22asqiuahb.ap-southeast-1.rds.amazonaws.com`, port 5432.

| Service | Database | Runtime role | Recorded migration state |
|---|---|---|---|
| User | `user_service_db` | `user_service_user` | Versions 1–5 applied; user-provided Goose output, 10 November |
| Supplier | `supplier_service_db` | `supplier_service_user` | Versions 1–2 applied; user-provided Goose output, 10 November |
| Credit | `creditservice` | `app_rw` | Versions 1–5 applied; user-provided Goose output, 10 November |
| Order | `order_service_db` | `order_service_user` | Versions 1–3 applied; direct SQL verification after trial and cleanup, 10 November |
| Admin | `admin_service_db` | `admin_service` | Version 1 applied; user-provided Goose output, 10 November |

The separate older `credit_service_db`, owned by `migrator`, contained only migration bookkeeping when checked and was left untouched. Do not point credit-service at it or delete it without checking usage.

Credit migrations used the existing `migrator` role. `app_rw` login, application table permissions, and required sequence permissions were checked; Goose bookkeeping is read-only for this role. The order runtime role currently owns its database; separate migration/runtime privileges have not been established there. Do not rerun a bootstrap that resets shared role passwords.

Admin's database/schema owner is `migrator`; its separate runtime login is `admin_service`. The user created the runtime role/password and applied the migration through Goose, authenticating as `CreditAdmin` with `options='-c role=migrator'` because the existing migrator password was unavailable. Do not store CreditAdmin credentials in application secrets or reset the shared migrator password. The migration grants runtime SELECT/INSERT/UPDATE on its fact tables. Keep runtime schema CREATE revoked and USAGE granted.

Order's completion-index migration originally duplicated saved-places version `00002`; it is now `00003_completion_flag_index.sql`. The one-off trial applied pending versions 2 and 3, and direct checks confirmed `saved_places` and `errand_transitions_completions_idx`, with `saved_places` owned by `order_service_user`. These schema changes remain after removal of automatic-migration infrastructure. See the cleanup record under [Manual database migrations](#manual-database-migrations).

Credit, order and admin Dockerfiles package the RDS CA bundle at `/etc/ssl/certs/rds-global-bundle.pem`; use `sslmode=verify-full` with `sslrootcert` pointing there. User/supplier runtime TLS verification has not been audited to the same level.

In DBeaver, use an absolute local path to the CA bundle. `./global-bundle.pem` may not resolve from its working directory. Find tables under the selected database's `Schemas → public → Tables` and refresh after migrations.

See [RDS migration notes](rds-goose-migrations.md) for the existing user/supplier procedure and each service README for its migration instructions.

## Amazon MQ

- Replacement broker: `foc-rabbitmq-1`, ID `b-5e3cb138-e1dd-488e-a93f-ea99548d42b4`; verified RUNNING on 10 November. The previous broker (`foc-rabbitmq`, ID `b-2073f411-5a0f-4452-ac34-7406eac4ff7f`) was deleted and its endpoint is obsolete.
- RabbitMQ 4.3, `mq.m7g.medium`, single instance in `ap-southeast-1c`, privately accessible.
- Endpoint: `amqps://b-5e3cb138-e1dd-488e-a93f-ea99548d42b4.mq.ap-southeast-1.on.aws:5671`.
- Private subnet: `subnet-0201f7b8f28efc2b4`; attached security group: `sg-014bd894dbdad3f72`. Permit AMQPS 5671 from the application security group; no ALB is involved.
- RabbitMQ username/password authentication is separate from IAM. Store the authenticated URL in Secrets Manager; percent-encode reserved password characters once (`!` becomes `%21`).
- Credit successfully logged `consuming credit commands` for `credit-service.commands` after correcting credentials. Old-task retry/shutdown messages appeared alongside new-task success; compare task IDs.
- Order readiness reported `creditResults: running`, `postgres: ok`, `rabbitmq: ok`, `redis: disabled`, `supplier: closed`. `closed` describes the supplier circuit breaker; it does not prove an authorized supplier request succeeded.
- Order uses `AMQP_DECLARE_TAP=true` for the current event-tap topology. Redis is intentionally disabled.
- When replacing a broker, update only `AMQP_URL` in the credit, order and admin runtime secrets, preserve their other keys, and force new ECS deployments so tasks reload the values. Applications declare their durable topology; the RabbitMQ user needs configure/write/read permissions on `/`. Deleted-broker messages do not return when a replacement broker is created: review in-flight errands and use the supported history backfill/reconciliation procedures where needed.

Standard CloudShell is outside this VPC. Independent broker tests need a VPC-connected environment/task and TLS/AMQP authentication. Do not deploy the local RabbitMQ container to ECS alongside Amazon MQ.

## Secrets and wallet provisioning

| Secret | JSON keys used |
|---|---|
| `foc/credit-service/runtime` | `DATABASE_URL`, `AMQP_URL`, `CREDIT_M2M_CLIENT_ID`, `CREDIT_M2M_CLIENT_SECRET` |
| `foc/order-service/runtime` | `DATABASE_URL`, `AMQP_URL`, `ORDER_M2M_CLIENT_ID`, `ORDER_M2M_CLIENT_SECRET` |
| `foc/admin-service/runtime` | `DATABASE_URL`, `AMQP_URL`, `ADMIN_M2M_CLIENT_ID`, `ADMIN_M2M_CLIENT_SECRET` |

Admin runtime secret ARN: `arn:aws:secretsmanager:ap-southeast-1:842761392141:secret:foc/admin-service/runtime-7gvzpj`. Its database connection must use the runtime `admin_service` role, not CreditAdmin or the migrator. The separate experimental `foc/migrations/runtime` secret is scheduled for deletion and must not be referenced by active task definitions.

User-service reuses the credit secret's `CREDIT_M2M_*` keys for wallet provisioning. These are Auth0 machine-to-machine credentials, separate from AWS IAM and browser authentication.

- Execution role `ecsTaskExecutionRole` has permission to retrieve the runtime secrets, alongside image/logging permissions.
- JSON key names must match references exactly. `CREDIT_M2M_SECRET` failed because the saved key was `CREDIT_M2M_CLIENT_SECRET`.
- Secrets are injected at task startup: force a new deployment after changing values.
- Order revision 2 replaced plaintext environment credentials with secret references. Older task revisions may retain earlier values; cleanup and credential rotation remain outstanding.
- The supplier task role's S3 upload permission is separate from execution-role secret access.

User-service is configured with:

```text
CREDIT_SERVICE_URL=http://credit-service:3004
CREDIT_AUDIENCE=https://api.foc.local/credit-service
```

Its `CREDIT_M2M_CLIENT_ID` and `CREDIT_M2M_CLIENT_SECRET` are secret references. Credit uses `INITIAL_CREDIT_AMOUNT=100`. Provisioning is idempotent and also handles existing users when they provision again; retries must not grant another initial balance. End-to-end wallet creation and the M2M grant still need verification.

## Auth0 browser issue and custom-domain rollout

Tenant: `dev-nj7qnvzgpr2panhl.us.auth0.com`; application: `FOC Auth0`.

The frontend uses `auth0.js` embedded email/password login with implicit responses (`token id_token`). Background token acquisition uses iframe-based `checkSession` with `prompt=none`. Before the custom-domain rollout, frontend and Auth0 had different sites (`cloudfront.net` and `auth0.com`).

Observed failures:

- Embedded login: `fcoa`, `Unable to configure verification page.`
- Silent authentication: `fsa`, `login_required`.
- Supplier connection messages could appear when token acquisition failed before an API request was sent.

On 5 November, temporarily allowing third-party cookies made embedded login and supplier loading work. This confirms the cookie dependency; local Compose working did not rule out a deployed-origin browser issue. These errors did not establish a backend M2M failure. The missing `favicon.ico` 403 was unrelated.

A silent `login_required` before signing in is expected; after login it can also indicate that the iframe cannot access the session. Silent authentication can return HTTP 200 HTML containing an error, so inspect the auth result/logs rather than status alone.

### Shared-domain setup

The embedded form is retained. The user registered `bestfoc.app` through Name.com; its authoritative DNS is hosted there.

| Component | Address |
|---|---|
| CloudFront frontend | `https://app.bestfoc.app` |
| Auth0 custom domain | `https://login.bestfoc.app` |

Both share the registrable parent domain `bestfoc.app`, addressing the cross-site cookie dependency when the app is accessed through `app.bestfoc.app`. Using the original CloudFront hostname with the custom Auth0 domain is still cross-site.

DNS records added through Name.com:

| Type | Host relative to `bestfoc.app` | Target |
|---|---|---|
| CNAME | `app` | `d8d2ig90yjarp.cloudfront.net` |
| CNAME | `_edc271adda9e81035e2c09fb5b0d7700.app` | `_027028ebba10473698abb1208d7ad014.wzccmgtwzk.acm-validations.aws` |
| CNAME | `login` | `dev-nj7qnvzgpr2panhl-cd-qcdsq35pmjy7fuov.edge.tenants.us.auth0.com` |

The frontend certificate was requested in ACM `us-east-1`, required for CloudFront viewer HTTPS, and the user confirmed it reached **Issued**. Auth0 uses its own Auth0-managed certificate; the user confirmed the custom domain reached verified status. Keep the ACM validation and Auth0 CNAME records for certificate renewal. Public DNS lookups confirmed both validation targets during setup.

The school role's IAM simulation showed `cloudfront:UpdateDistribution` allowed with organization policies enabled; the user also reported `acm:RequestCertificate` allowed. `route53domains:ListDomains` was denied by AWS Organizations. Registration and DNS management through Name.com avoided needing Route 53 Domains.

The user created new ECS task-definition revisions and reported all four services deployed. The instructed environment changes were:

| Task definition | Variable | Value |
|---|---|---|
| `user-service-task` | `AUTH0_DOMAIN` | `login.bestfoc.app` |
| `supplier-service-task` | `AUTH0_ISSUER` | `https://login.bestfoc.app/` |
| `credit-service-task` | `AUTH0_ISSUER` | `https://login.bestfoc.app/` |
| `order-service-task` | `AUTH0_ISSUER` | `https://login.bestfoc.app/` |

Tokens obtained through the custom domain carry `https://login.bestfoc.app/` as issuer. Existing API audience identifiers remain unchanged. Issuer changes also affect the services' outbound M2M token requests; cross-service requests must be verified after rollout. Exact live task-definition values have not been independently rechecked.

A read-only request to `https://app.bestfoc.app/api/auth/config` returned HTTP 200 with:

```json
{"domain":"login.bestfoc.app","clientId":"nFcNCHhBlYEkxwwewFt02hjWUmxjVg4x","audience":"https://api.foc.local/user-service"}
```

The frontend reads its Auth0 domain from this endpoint; this confirms the deployed user-service publishes the custom domain, not that authentication or all API workflows succeed.

The user confirmed adding these Auth0 application settings:

| Auth0 setting | Value |
|---|---|
| Allowed Callback URLs | `https://app.bestfoc.app/` |
| Allowed Logout URLs | `https://app.bestfoc.app/` |
| Allowed Web Origins | `https://app.bestfoc.app` |
| Allowed Origins (CORS) | `https://app.bestfoc.app` |

These were added alongside existing entries; keep required localhost entries. Callback/logout URLs must match what the app sends; origins have no path or trailing slash. Saved dashboard values have not been independently rechecked.

### User-service Management API audience

`user-service` now reads `AUTH0_MANAGEMENT_API_AUDIENCE` separately from `AUTH0_DOMAIN`. Keep `AUTH0_DOMAIN=login.bestfoc.app` for login and Management API requests, and set the audience to `https://dev-nj7qnvzgpr2panhl.us.auth0.com/api/v2/`. Auth0 requires the canonical tenant identifier as the Management API audience even when using a custom domain.

The code change is local and uncommitted. Task revision `user-service-task:8` is registered with the correct audience but uses the existing image; the ECS service still runs revision `:7`. Build and deploy the updated user-service image using revision `:8` as the base before expecting administrator user listings or role changes to work. The CD script bases releases on the revision selected by the service, so deploying before `:8` is selected would omit this variable. Also verify the M2M application's Management API grants include `read:roles` and `read:role_members` for role-filtered listings, `read:users` for directory searches, and `update:users` for role changes.

References: [Auth0 Management API audience with custom domains](https://support.auth0.com/center/s/article/Service-not-enabled-within-domain), [Auth0 custom-domain Management API requests](https://support.auth0.com/center/s/article/Access-token-rejected), [custom domains and token issuers](https://auth0.com/docs/customize/custom-domains).

## Admin service deployment

Admin-service is deployed. It maintains the event-fed read model for **Admin → Metrics** and checks service readiness for **Admin → Overview**. Live counters on Overview still come from order-service's `/admin/overview`; numeric metrics come from admin's fact tables, not from readiness probes.

| Resource | Verified configuration |
|---|---|
| ECS service | `admin-service-task-service-vmoebao1` in `comfortable-squirrel-pxwxt5` |
| Active task revision | `admin-service-task:3`, Fargate ARM64, port 3005 |
| ECR repository / build context | `admin-service` / `admin-service/` |
| Database / runtime role | `admin_service_db` / `admin_service`; schema owner `migrator` |
| Runtime secret | `foc/admin-service/runtime` (ARN suffix `7gvzpj`) |
| Service Connect | Client only in `foc-local`; reaches user-service through `user-service-sc:8080` |
| Task security groups | `sg-0badb0855aab50221`, `sg-098b424831646a677`; the latter is allowed to reach user-service on TCP 8080 |
| ALB | Rule priority 50, `/admin-service/*`, strip prefix, `admin-service-tg:3005`, `/readyz` HTTP 200 |
| CloudFront | `/admin-service/*` → `foc-alb-api`, caching disabled, `AllViewerExceptHostHeader` |
| Deployment configuration | `.github/deploy.json` uses the actual generated ECS service name above |

The initial startup failed because `AUTH0_ISSUER`, `ADMIN_M2M_CLIENT_ID`, and `ADMIN_M2M_CLIENT_SECRET` were missing. A successful image build or ECS start request does not prove the process stays running. Correct task environment/secret references, deploy a new revision, and inspect stopped-task details and CloudWatch logs. The production readiness endpoint subsequently reported `postgres: ok` and `events: running`; the all-component CD rollout also passed.

Required non-secret configuration:

```text
APP_ENV=production
HTTP_HOST=0.0.0.0
HTTP_PORT=3005
AUTH0_ISSUER=https://login.bestfoc.app/
AUTH0_AUDIENCE=https://api.foc.local/admin-service
ORDER_AUDIENCE=https://api.foc.local/order-service
ORDER_SERVICE_URL=http://order-service:3003
USER_SERVICE_URL=http://user-service-sc:8080
SUPPLIER_SERVICE_URL=http://supplier-service:3002
CREDIT_SERVICE_URL=http://credit-service:3004
DASHBOARD_TIMEZONE=Asia/Singapore
```

The user confirmed Auth0 setup: the admin API has `dashboard:read` assigned to the Administrator role; admin's M2M application has `orders:admin-read` on the order-service API. These are separate from AWS IAM. Tenant grants and authenticated dashboard requests have not been independently exercised in this notes refresh.

Admin declares the quorum queue `admin-service.events` and its dead-letter queue, bound to `errand.#` on `foc.events`. Credit, order and admin must all reference the replacement broker. Backfill must begin only after the admin consumer binds its queue. Historical backfill/enrichment remains unverified; zero metrics do not by themselves mean the deployment failed. Use the [admin README](../admin-service/README.md#backfill) and a verified connection to order-service. The image contains `/app/admin-backfill`; changing only container `command` will not replace its default `/app/admin-service` entrypoint. A VPC backfill task also needs a working route/name for order-service; do not assume a standalone task automatically inherits an ECS service's Service Connect configuration.

### Readiness versus health

The readiness checker appends `/readyz` to each configured service base URL. User-service is intentionally checked at **`http://user-service-sc:8080/readyz`**, which calls `pool.Ping`: HTTP 200 `{"status":"ready"}` or HTTP 503 `{"status":"not_ready"}`. Its ALB health check remains `/health`, which returns HTTP 204 and only establishes that the process responds. Changing the admin checker to `/health` would lose the database-readiness check and conflict with its expected HTTP 200 response. Metrics are computed separately from stored events. User-service's `/readyz` is internal here; the public ALB rule still exposes `/api/*` and `/health`.

### Frontend and local Compose changes

PR #102 included removal of credit balance/account ID from Profile and the supplier Record IDs, a supplier-detail action that opens an errand with that supplier selected, searchable pickup/building inputs, and the sticky desktop sidebar with accessible logout. The successful `all` deployment included the frontend at `37b25c8`; it was not skipped. Later UI rollback was discussed but no frontend revert was performed. Browser interaction after the AWS rollout has not been checked.

For a local frontend refresh, from the repository root:

```sh
docker compose up -d --build --no-deps user-frontend
```

Hard-refresh the browser afterward. `docker compose up -d` alone does not rebuild changed source. If recreated backends are healthy but Nginx returns 502 to their old container IPs, restart `user-frontend` to refresh its static upstream resolutions. The admin proxy already uses Docker DNS dynamically so a stopped admin-service does not stop the entire frontend. Local admin-service exits if its `.env` lacks the required Auth0 settings; that is distinct from an Nginx stale-IP failure. Preserve named database volumes while diagnosing; `down -v` deletes local data.

## Remaining work

1. Verify the reported ECS domain rollout and test embedded login, profile/supplier/credit/order reads, page-refresh session restoration, token renewal, and logout/login at `https://app.bestfoc.app` with third-party cookies blocked. Deploy the local user-service Management API audience fix using task revision `:8` as the base, then verify administrator listings and role changes.
2. Verify new/existing-user wallet provisioning, initial balance of 100 exactly once, and required Auth0 M2M permissions.
3. Test credit reads and the order lifecycle through CloudFront, including RabbitMQ commands/results. Readiness alone does not verify these workflows.
4. Review remaining integration settings, including supplier-to-order checks, before relying on those workflows in production.
5. Configure CloudFront-to-ALB HTTPS and review direct ALB access during domain setup.
6. Review historical plaintext credentials/task revisions, unused infrastructure, and user/supplier database TLS verification; rotate credentials where needed.
7. Commit/merge the repository's deployment-policy correction so a future console replacement cannot restore the old `DescribeTaskDefinition` denial. Commit `6827936` fixes the saved-place timestamp issue but is not in the deployed `37b25c8` source; merge/deploy it through the normal workflow. Retain manual migrations; the automatic-migration experiment has been removed from AWS.
8. Complete admin historical backfill/enrichment and verify authorized dashboard metrics/readiness. SPA deep-link fallback is still absent from the default CloudFront behavior. Review rollout duration before changing target draining times; all seven target groups are still 300 seconds.

RDS, ALB, NAT/Elastic IP, and Amazon MQ can keep incurring charges when ECS desired counts are zero. Stopping application tasks does not pause the entire deployment.

## GitHub Actions CI/CD

Workflows are live on the public team repository `AY2627S1-CS3219-P33/FoC-Template`, default branch `main`. PR #102 merged on 10 November. The `production` environment has `AWS_DEPLOY_ROLE_ARN=arn:aws:iam::842761392141:role/foc-github-deploy`, with OIDC trust checked against the immutable repository/environment subject. This local update adds chat and notification to CI/CD; it is not merged to `main` yet. There is no separate admin workflow.

- [CI](../.github/workflows/ci.yml) runs on pull requests and pushes to `main`. It regenerates supplier, order and admin SQL query code as their Docker builds do, then checks Go formatting, vet, tests, and builds for all seven services, plus frontend type checking/build, OpenAPI lint, workflow lint, and deployment-script tests. Disposable PostgreSQL 18, RabbitMQ 4, and Redis 7.4 containers enable the existing integration tests. The explicit database-restart test remains opt-in.
- [Deploy](../.github/workflows/deploy.yml) runs only when manually triggered on the team repository's `main` branch. Deploy operations rerun CI on that exact commit before requesting AWS credentials. Rollback skips source-code CI and restores recorded release artifacts instead.
- [Deployment configuration](../.github/deploy.json) contains AWS identifiers and public check URLs for all seven backends. Chat and notification are included in the deploy selector and the ECR, task-family, and ECS service scopes. [The script](../.github/scripts/deploy.py) uses AWS CLI commands, builds backend images on an ARM64 runner, and creates new task-definition revisions from the service's currently selected revision. Environment variables, secret references, task roles, resource limits, tags, and service configuration are preserved; only the application image changes. It does not choose the highest-numbered unrelated revision.

### Environment setup and recovery reference

1. Review the deployment configuration against the live account. Confirm each service uses an ARM64 task and the matching ECR repository. Fix the outstanding user-service Management API issue separately; CI/CD does not fix application bugs.
2. In the **team repository → Settings → Environments**, create `production`. Under deployment branches/tags, allow only the branch `main`. This restriction is required because the OIDC trust identifies the environment rather than the branch. Optionally add required reviewers. Protect `main` with required CI checks using the repository's branch rules.
3. In **AWS IAM → Identity providers**, add or reuse the OpenID Connect provider `https://token.actions.githubusercontent.com`, with audience `sts.amazonaws.com`.
4. In **IAM → Roles**, create a dedicated role such as `foc-github-deploy`, using [deploy-trust.json](../.github/aws/deploy-trust.json) as its trust relationship. A read-only GitHub API check confirmed this repository uses the immutable subject prefix `repo:AY2627S1-CS3219-P33@330282387/FoC-Template@1373992806`; do not replace it with the older name-only format. Recheck if the repository is renamed, transferred, or its OIDC configuration changes.
5. The customer-managed `foc-github-deploy-permissions` policy is attached to the role. Live policy version `v5` now includes chat and notification ECR repositories, task families and ECS services. Keep `iam:PassRole` scoped to the actual application execution/task roles (`ecsTaskExecutionRole` and `foc-supplier-task-role`). `ecs:DescribeTaskDefinition` requires a separate `Resource: "*"` read statement; tagging and service updates remain ARN-scoped. No automatic-migration permissions or secret-value reads belong in this policy.
6. In **GitHub → production environment → Environment variables**, set `AWS_DEPLOY_ROLE_ARN` to the new role ARN. Do not add AWS access keys or application secrets to GitHub. Existing application secrets remain in ECS references and are retrieved by task execution roles.
7. Merge the updated workflows and policy template into the team repository's `main` branch, confirm CI succeeds, then trigger deployments for chat and notification. Their database migrations are already applied; check the migration list and confirm `migrations_applied` for each first tracked deployment. The live IAM policy was updated separately in AWS.

Only the frontend and deployment metadata prefixes are writable by the role. Supplier image keys at the bucket root and under `suppliers/` are outside its object permissions. No object deletion permission is needed. These JSON files are the repository copies of the configured role/policy; they are not infrastructure-as-code that automatically reconciles AWS.

Sources: [ECS IAM action/resource support](https://docs.aws.amazon.com/service-authorization/latest/reference/list_ecs.html), [GitHub OIDC with AWS](https://docs.github.com/en/actions/how-tos/secure-your-work/security-harden-deployments/oidc-in-aws), [immutable OIDC subjects](https://docs.github.com/en/actions/reference/security/oidc), [ECR push permissions](https://docs.aws.amazon.com/AmazonECR/latest/userguide/image-push-iam.html), [ECS deployment permissions](https://github.com/aws-actions/amazon-ecs-deploy-task-definition#permissions).

### Deploying a change

Open **team repository → Actions → Deploy → Run workflow**, select branch `main`, and use operation `deploy`.

| Component selection | Behavior |
|---|---|
| `auto` | Deploy components with no recorded release, or whose build inputs changed since their own release |
| A named service or `frontend` | Redeploy just that component, even if its source has not changed |
| `all` | Deploy every component |

Each service owns its folder; frontend owns `user-frontend/`. Supplier also depends on `data/csv/supplier-seed-data.csv` and the root `.dockerignore`. Changes to deployment configuration and GitHub workflow/script files do not rebuild application images. Markdown-only changes do not trigger deployments. CI still checks all components.

State is stored at `s3://foc-supplier-images/deployments/state/<component>.json`, including the source commit and one previous release. Change detection compares the full Git trees against each component's record, so multiple commits accumulate and deleted files are included. Missing history errors stop the workflow rather than silently skipping changes. Components without a state record are selected by `auto`; this lets chat and notification be deployed on their first tracked run without redeploying unrelated services. All existing migration SQL files are flagged for that component's first deployment, even if already applied.

For a backend, the workflow builds and pushes a uniquely tagged image (`<commit>-<run-id>-<attempt>`), then uses its immutable digest in the new revision. It updates only the selected ECS service, waits for stability while running, and verifies ECS retained the requested revision rather than automatically rolling back. Public checks use the configured JSON endpoints. These checks establish deployment/readiness, not authenticated business-workflow correctness; order readiness can return HTTP 200 while reporting degraded dependencies.

ECS desired counts are never supplied to `update-service`. A service at zero stays stopped and is reported as **updated while stopped; runtime checks skipped**. Its configuration baseline is recorded after the revision is selected; this is not proof the image starts. When you later scale it up, ECS uses that selected revision. Auto Scaling or manual actions can independently change counts; avoid console deployments during a workflow run. GitHub serializes deploy/rollback runs without cancelling a deployment already in progress. Multi-component releases are sequential and not atomic; successful components retain their records if a later component fails.

For the frontend, the workflow builds static content, saves a release snapshot under `deployments/frontend/<release>/`, uploads non-HTML assets before `frontend/index.html`, and invalidates `/` and `/frontend/index.html`. It waits for invalidation completion and verifies the served HTML hash. Uploads never delete objects, so old browser sessions retain their hashed assets and supplier images remain untouched. Old snapshots/assets and ECR images consume storage; cleanup remains manual and must preserve rollback releases. Do not apply a bucket lifecycle policy that deletes `deployments/state/`.

### Manual database migrations

If selected components have changed migration SQL, the workflow lists the files and stops before any build/push/deployment unless **migrations_applied** is checked. This checkbox is the operator's attestation, not a database query or migration runner.

1. Review the exact workflow commit and listed migrations. For a first deployment, compare all listed files against the database's existing Goose versions.
2. From an authorized machine with RDS network access, use each service's documented Goose procedure with its migration credentials: [user](../user-service/migrations/README.md), [supplier](../supplier-service/migrations/README.md), [credit](../credit-service/README.md), [order](../order-service/migrations/README.md), and [admin](../admin-service/migrations/README.md). Check `status`, apply only pending migrations with `up`, and confirm `status` afterward. Use TLS verification and the intended service database; do not reset users or bootstrap existing databases.
3. Keep migrations backward compatible with running application versions. If that is impossible, plan downtime and explicitly stop affected services before applying them. Never assume an application rollback reverses schema or data changes.
4. Trigger Deploy again on the same reviewed commit with **migrations_applied** checked. If `main` advanced, review and apply any additional migrations before confirming. A rerun of the original workflow retains its commit; a fresh dispatch selects the current branch commit.

RDS does not need to be exposed to GitHub runners. Manual migrations are the chosen final approach. Local Compose's one-shot migration containers do not make AWS CD run Goose. Checking **migrations_applied** does not query RDS, validate tables, compare SQL contents, or apply migrations; it is the operator's attestation. A first recorded release flags all SQL files even when RDS already has them. Goose records versions, not file-content checksums: inspect schema objects too when resolving a numbering collision.

For CLI checks, run the pinned Goose `status` command against each database using its service's migration directory, TLS verification and locally supplied credentials (`~/.pgpass` or a temporary `PGPASSWORD`). Do not print or commit passwords. The database map is in the RDS table above. If the shared `migrator` password is unavailable, the previously verified manual method authenticates as CreditAdmin with connection `options='-c role=migrator'` for admin/credit, preserving ownership without resetting the shared password. Order's owner is `order_service_user`; user/supplier have their corresponding owner roles. Always check `session_user`, `current_user` and `current_database()` before applying SQL as an assumed owner.

### Automatic-migration experiment and removal

The experiment is isolated in branch `feat/automatic-ecs-migrations`, commit `4d1ccab`. Do not merge it while manual migrations remain the decision. One ARM64 Fargate task (0.25 vCPU / 0.5 GB) ran order Goose versions 2 and 3 successfully and stopped with exit code 0. No application ECS service was created for migration, and no application revision was changed by the trial. Applied schema changes were preserved during cleanup.

Resources added and subsequently removed on 10 November:

| Experimental resource | Cleanup outcome |
|---|---|
| PostgreSQL login `foc_deploy_migrator` | CONNECT grants and login removed; original owner/runtime roles and passwords preserved |
| IAM role `foc-migration-execution` | Removed, including inline `ReadMigrationCredentials` and its attachment to the AWS-managed execution policy |
| Secret `foc/migrations/runtime` (ARN suffix `VNfw4X`) | Scheduled for deletion on 17 November 2026, with seven-day recovery window; must not be used by active tasks |
| Task definition `order-service-migrate:1` | Deregistered; deletion requested, last observed `DELETE_IN_PROGRESS` |
| One-off task `7cec226b6c2d49ef9977d3070d07d654` | Stopped successfully; historical ECS record can remain temporarily |
| ECR image `order-service:migration-trial-8e6a17c0` | Deleted; application images/repository preserved |
| Log stream `migrations/migrate/7cec226b6c2d49ef9977d3070d07d654` | Deleted from the existing `/ecs/order-service-task` group; group preserved |
| Deployment-policy version `v3` | Removed and `v2` restored, preserving admin permissions; later `v4` adds only the required task-definition read correction |

There is no running migration compute. No new RDS instance, ECR repository, cluster, VPC, subnet, NAT gateway, ALB or log group was created for the experiment. Automatic-migration code must not be used against the removed IAM role/secret.

### Rollback and recovery

In **Actions → Deploy → Run workflow**, choose operation `rollback`, select exactly one named component, and check **migrations_applied** only after confirming the database remains compatible with the older application. `auto` and `all` are rejected for rollback.

If the live service revision or frontend HTML differs from the last recorded successful release, rollback restores that record (for example, after a failed deployment). Otherwise, it restores the record's predecessor. Backend rollback selects the recorded task-definition ARN and preserves desired count; frontend rollback republishes the saved snapshot and invalidates the entry page. Neither rebuilds an old image or changes the database. The first backend deployment records the pre-pipeline task revision for rollback, but its source commit is unknown; after restoring it, the next deployment rechecks the component's full tree. Frontend rollback requires a tracked snapshot: the pre-pipeline frontend is not backed up automatically, so the first frontend release has no predecessor snapshot.

State is written only after the selected revision/public checks succeed (or revision selection for a stopped service). A failure can still leave an image, task revision, frontend snapshot, or partially updated live deployment behind. Check the workflow summary and ECS events; do not treat a red run as proof that AWS made no changes. Do not delete previous task definitions, ECR digests, or frontend snapshots while they are needed for rollback.

If ECS already automatically restored the last successful revision, no further rollback is needed. An explicit rollback while that recorded revision is live intentionally moves to its predecessor.

### Local verification

```sh
python3 -m unittest discover -s .github/scripts -p 'test_*.py' -v
go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7 .github/workflows/ci.yml .github/workflows/deploy.yml
```

The script tests exercise accumulated change selection, shared inputs, manual migration blocking, fork/branch restrictions, task-definition preservation, zero desired counts, ECS rollback detection, safe frontend uploads, failed-release state handling, recorded rollback, and the required wildcard task-definition read permission. All 16 local deployment tests pass after the IAM regression check was added. Policy validation reported no errors, IAM simulation changed the denied read to allowed, and the subsequent GitHub deployment succeeded.

The failed [deployment run](https://github.com/AY2627S1-CS3219-P33/FoC-Template/actions/runs/42768001825) attempt 1 stopped before image build at the user-service preflight. The live policy incorrectly put `ecs:DescribeTaskDefinition` under task-definition ARNs; AWS does not support resource scoping for that action. Version `v4` adds its wildcard read statement, leaving tagging/service writes scoped. CloudTrail lookup was explicitly denied by a school service-control policy, so the original event error could not be fetched. The corrected role passed preflight for all five services, and attempt 2 completed the all-component deploy. Docker's temporary credential-file warning on the hosted runner was not the cause.

The all-component deployment stage ran approximately 14:32–15:22 Singapore time, with all backend rollout/public checks and frontend HTML verification successful. This is deployment evidence, not proof of login, authorized mutations, notifications, or complete order lifecycle behavior.

`TestPlacesCRUD` later exposed an intermittent saved-place timestamp mismatch: separate `clock_timestamp()` defaults could produce different initial created/updated times. Commit `6827936` on `fix/saved-place-creation-timestamps` changes only `order-service/queries/requester.sql` to use `statement_timestamp()` for both insert fields; no migration is required. The baseline failed three times in 200 local repetitions; the fix passed 200 repetitions and the requester-repository, places, test-fakes and migration suites against disposable PostgreSQL 18. Generated sqlc files are ignored and regenerated by CI/builds. The fix is not part of deployed commit `37b25c8`. A review found the same independent defaults in `InsertFence`, but no matching equality assertion/functional failure was demonstrated and no fence query was changed.

Local verification passed deployment-script tests, actionlint, frontend type checking/build, and OpenAPI validation (with existing warnings). Supplier, credit, and order tests passed with disposable PostgreSQL, RabbitMQ, and Redis dependencies. Enabling user-service's database tests exposed two outdated assertions, which have now been corrected. After the user confirmed soft deletion is intentional, `TestPostgresServiceFlow` was updated to check that unconfirmed deletion leaves the account accessible, confirmed deletion retains an inactive row with a deletion timestamp and unchanged username/email, and profile reads then return not-found. `TestBootstrapConflictAndRollback` now expects the pre-existing matching account in its subject-conflict case, verifies existing accounts remain unchanged, and still requires bootstrap completion to remain unset. The full user-service suite, vet, and build pass with disposable PostgreSQL as of 6 November. Those 6 November user-service changes adjusted tests, not application behavior. The 10 November deployment and IAM verification results above supersede the earlier pending-deployment status.
