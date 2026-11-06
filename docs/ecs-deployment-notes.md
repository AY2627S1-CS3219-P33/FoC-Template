# ECS deployment notes

Updated 6 November 2026. This records the settings inspected and results reported during the deployment session. It is not infrastructure as code or a fresh audit of the AWS account. Do not add passwords, secret values, or bearer tokens.

## Current deployment

- Account: `842761392141`; Region: `ap-southeast-1`.
- ECS cluster: `comfortable-squirrel-pxwxt5`.
- User, supplier, credit, and order services run on Fargate in private subnets, with public IP assignment disabled and ARM64 task architecture.
- The frontend is static content in S3, served through CloudFront. The old frontend ECS service was removed.
- Browser entry point: <https://app.bestfoc.app/>. The original distribution address is `d8d2ig90yjarp.cloudfront.net`; use the custom frontend domain for same-site Auth0 login.
- CloudFront forwards API requests to the public ALB, which forwards to private ECS task IPs. Tasks use the NAT gateway for outbound internet access, including Auth0.
- Amazon MQ provides RabbitMQ. The shared RDS PostgreSQL instance holds the service databases.

Backend deployments were healthy at the earlier checks. The user has since confirmed custom-domain frontend HTTPS, Auth0 domain verification, and deployment of new task-definition revisions for all four services. The public auth configuration was independently checked and returns `login.bestfoc.app`. Embedded login, API access, and renewal with third-party cookies blocked still need verification.

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
| RDS | `sg-035aaddc7336f2898` | PostgreSQL 5432 from application security group |
| RabbitMQ | `sg-014bd894dbdad3f72` (`foc-rabbitmq-sg`) | AMQPS 5671 from application security group |

The broker also had the application security group attached at the last inspection. Verify attached groups and individual rules before editing. DBeaver development access depends on the developer's current public IP matching the permitted rule; do not allow PostgreSQL from all IPs.

## ECS services and images

The revisions and image tags below were recorded before the custom-domain rollout. The user reports all four services were redeployed afterward; their current revision numbers and image tags have not been rechecked.

| Service | ECS service name | Revision before domain rollout | Container port |
|---|---|---|---|
| User | `user-service-task-service-tb6pn0x0` | `user-service-task:5` | 8080 |
| Supplier | `supplier-service-task-service-ot78zbdx` | `supplier-service-task:6` | 3002 |
| Credit | `credit-service` | `credit-service-task:3` | 3004 |
| Order | `order-service-task-service-177de1f2` | `order-service-task:2` | 3003 |

ECR registry: `842761392141.dkr.ecr.ap-southeast-1.amazonaws.com`.

| Repository | Image tag recorded before domain rollout |
|---|---|
| `user-service` | `2e4860c-20261101163724` |
| `supplier-service` | `2e4860c-20261101163724` |
| `credit-service` | `credit-20261104214829` |
| `order-service` | `order-20261104234356` |

Build for `linux/arm64`. Supplier-service requires the repository root as build context so the seed CSV is included at `/app/data/supplier-seed-data.csv`:

```sh
docker build --platform linux/arm64 \
  -f supplier-service/Dockerfile \
  -t supplier-service:local .
```

The other service images use their service directories as build contexts. Publishing an image does not deploy it: select the task definition revision and update the ECS service.

### Service Connect

All four services have Service Connect enabled in namespace `foc-local`:

| Service | Internal address |
|---|---|
| User | `http://user-service:8080` |
| Supplier | `http://supplier-service:3002` |
| Credit | `http://credit-service:3004` |
| Order | `http://order-service:3003` |

These names work inside participating ECS tasks, not browsers. Deploy a new server endpoint before redeploying clients that need to discover it. When inspecting ECS API output, check `deployments[].serviceConnectConfiguration`; a missing service-level field does not mean Service Connect is disabled.

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

Target groups use IP targets registered by ECS. Listener port 80 can forward to a different container port. A fixed 404 default action was recommended after removing the frontend target; confirm that cleanup before changing the listener.

CloudFront distribution: `E39SHVSD0FXUB9` (`d8d2ig90yjarp.cloudfront.net`), with alternate domain `app.bestfoc.app`.

| Behavior | Origin | Configuration |
|---|---|---|
| `/assets/*` | `frontend-s3`, origin path `/frontend` | Static asset caching |
| `/api/*` | `foc-alb-api` | All seven HTTP methods; caching disabled |
| `/supplier-service/*` | `foc-alb-api` | All seven HTTP methods; caching disabled |
| `/credit-service/*` | `foc-alb-api` | All seven HTTP methods; caching disabled |
| `/order-service/*` | `foc-alb-api` | All seven HTTP methods; caching disabled |
| Default | Original S3 origin, no origin path | Existing images; default root object `frontend/index.html` |

API behaviors use `AllViewerExceptHostHeader`. ALB rules perform the prefix rewrites. No distribution-wide SPA error mapping was configured; such mappings can hide API/image errors.

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
| User | `user_service_db` | Existing service credentials | Previously reported version 5; not rechecked |
| Supplier | `supplier_service_db` | Existing service credentials | Previously reported version 2; not rechecked |
| Credit | `creditservice` | `app_rw` | Version 5 applied; privileges checked |
| Order | `order_service_db` | `order_service_user` | Initial migration applied; tables visible |

The separate older `credit_service_db`, owned by `migrator`, contained only migration bookkeeping when checked and was left untouched. Do not point credit-service at it or delete it without checking usage.

Credit migrations used the existing `migrator` role. `app_rw` login, application table permissions, and required sequence permissions were checked; Goose bookkeeping is read-only for this role. The order runtime role currently owns its database; separate migration/runtime privileges have not been established there. Do not rerun a bootstrap that resets shared role passwords.

Credit and order Dockerfiles now package the RDS CA bundle at `/etc/ssl/certs/rds-global-bundle.pem`. Their configured connection strings use `sslmode=verify-full` and `sslrootcert` pointing there. User/supplier runtime TLS verification has not been audited to the same level.

In DBeaver, use an absolute local path to the CA bundle. `./global-bundle.pem` may not resolve from its working directory. Find tables under the selected database's `Schemas → public → Tables` and refresh after migrations.

See [RDS migration notes](rds-goose-migrations.md) for the existing user/supplier procedure and each service README for its migration instructions.

## Amazon MQ

- Broker: `foc-rabbitmq`, ID `b-2073f411-5a0f-4452-ac34-7406eac4ff7f`.
- RabbitMQ 4.3, `mq.m7g.medium`, single instance in `ap-southeast-1c`, privately accessible.
- Endpoint: `amqps://b-2073f411-5a0f-4452-ac34-7406eac4ff7f.mq.ap-southeast-1.on.aws:5671`.
- RabbitMQ username/password authentication is separate from IAM. Store the authenticated URL in Secrets Manager; percent-encode reserved password characters once (`!` becomes `%21`).
- Credit successfully logged `consuming credit commands` for `credit-service.commands` after correcting credentials. Old-task retry/shutdown messages appeared alongside new-task success; compare task IDs.
- Order readiness reported `creditResults: running`, `postgres: ok`, `rabbitmq: ok`, `redis: disabled`, `supplier: closed`. `closed` describes the supplier circuit breaker; it does not prove an authorized supplier request succeeded.
- Order uses `AMQP_DECLARE_TAP=true` for the current event-tap topology. Redis is intentionally disabled.

Standard CloudShell is outside this VPC. Independent broker tests need a VPC-connected environment/task and TLS/AMQP authentication. Do not deploy the local RabbitMQ container to ECS alongside Amazon MQ.

## Secrets and wallet provisioning

| Secret | JSON keys used |
|---|---|
| `foc/credit-service/runtime` | `DATABASE_URL`, `AMQP_URL`, `CREDIT_M2M_CLIENT_ID`, `CREDIT_M2M_CLIENT_SECRET` |
| `foc/order-service/runtime` | `DATABASE_URL`, `AMQP_URL`, `ORDER_M2M_CLIENT_ID`, `ORDER_M2M_CLIENT_SECRET` |

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

### Outstanding user-service Management API issue

Local code inspection found that `user-service/internal/auth/management.go` derives both Management API request URLs and its audience from `AUTH0_DOMAIN`. With `AUTH0_DOMAIN=login.bestfoc.app`, it requests the incorrect audience `https://login.bestfoc.app/api/v2/`. The Management API audience must remain `https://dev-nj7qnvzgpr2panhl.us.auth0.com/api/v2/`.

Management API calls can use either the original tenant domain or the custom domain, but token requests and API requests must use the same domain and retain the original audience identifier. Separate the management configuration or audience from the browser login domain, test the fix, and deploy a new user-service image. This fix has not been implemented. Administrator user listings and role changes may fail; ordinary login does not exercise these Management API calls.

References: [Auth0 cross-origin authentication](https://auth0.com/docs/authenticate/login/cross-origin-authentication), [silent authentication](https://auth0.com/docs/authenticate/login/configure-silent-authentication), [custom domains and token issuers](https://auth0.com/docs/customize/custom-domains), [custom-domain API configuration](https://auth0.com/docs/customize/custom-domains/configure-features-to-use-custom-domains).

## Remaining work

1. Verify the reported ECS domain rollout and test embedded login, profile/supplier/credit/order reads, page-refresh session restoration, token renewal, and logout/login at `https://app.bestfoc.app` with third-party cookies blocked. Fix and deploy the user-service Management API audience issue, then verify administrator listings and role changes.
2. Verify new/existing-user wallet provisioning, initial balance of 100 exactly once, and required Auth0 M2M permissions.
3. Test credit reads and the order lifecycle through CloudFront, including RabbitMQ commands/results. Readiness alone does not verify these workflows.
4. Review remaining integration settings, including supplier-to-order checks, before relying on those workflows in production.
5. Configure CloudFront-to-ALB HTTPS and review direct ALB access during domain setup.
6. Review historical plaintext credentials/task revisions, unused infrastructure, and user/supplier database TLS verification; rotate credentials where needed.
7. Configure and exercise the repository CI/CD workflows described below. Automated migration tasks remain deferred; database migrations are manual.

RDS, ALB, NAT/Elastic IP, and Amazon MQ can keep incurring charges when ECS desired counts are zero. Stopping application tasks does not pause the entire deployment.

## GitHub Actions CI/CD

Workflows are implemented locally for the public team repository `AY2627S1-CS3219-P33/FoC-Template`, whose default branch is `main`. They have not been pushed or run on GitHub, and no IAM or GitHub environment configuration has been applied by this implementation.

- [CI](../.github/workflows/ci.yml) runs on pull requests and pushes to `main`. It regenerates supplier/order SQL query code as their Docker builds do, then checks Go formatting, vet, tests, and builds for all four services, plus frontend type checking/build, OpenAPI lint, workflow lint, and deployment-script tests. Disposable PostgreSQL 18, RabbitMQ 4, and Redis 7.4 containers enable the existing integration tests. The explicit database-restart test remains opt-in.
- [Deploy](../.github/workflows/deploy.yml) runs only when manually triggered on the team repository's `main` branch. Deploy operations rerun CI on that exact commit before requesting AWS credentials. Rollback skips source-code CI and restores recorded release artifacts instead.
- [Deployment configuration](../.github/deploy.json) contains the existing AWS identifiers and public check URLs. [The script](../.github/scripts/deploy.py) uses AWS CLI commands, builds backend images on an ARM64 runner, and creates new task-definition revisions from the service's currently selected revision. Environment variables, secret references, task roles, resource limits, tags, and service configuration are preserved; only the application image changes. It does not choose the highest-numbered unrelated revision.

### One-time console setup

1. Review the deployment configuration against the live account. Confirm each service uses an ARM64 task and the matching ECR repository. Fix the outstanding user-service Management API issue separately; CI/CD does not fix application bugs.
2. In the **team repository → Settings → Environments**, create `production`. Under deployment branches/tags, allow only the branch `main`. This restriction is required because the OIDC trust identifies the environment rather than the branch. Optionally add required reviewers. Protect `main` with required CI checks using the repository's branch rules.
3. In **AWS IAM → Identity providers**, add or reuse the OpenID Connect provider `https://token.actions.githubusercontent.com`, with audience `sts.amazonaws.com`.
4. In **IAM → Roles**, create a dedicated role such as `foc-github-deploy`, using [deploy-trust.json](../.github/aws/deploy-trust.json) as its trust relationship. A read-only GitHub API check confirmed this repository uses the immutable subject prefix `repo:AY2627S1-CS3219-P33@330282387/FoC-Template@1373992806`; do not replace it with the older name-only format. Recheck if the repository is renamed, transferred, or its OIDC configuration changes.
5. Create and attach a customer-managed permissions policy from [deploy-permissions.json](../.github/aws/deploy-permissions.json). Before attaching, confirm its `iam:PassRole` list includes exactly the execution/task role ARNs in the four live task definitions. The template uses the two roles recorded in these notes; add any other actual roles individually, never a wildcard. ECR login and task-definition registration require wildcard resources; other permissions target the existing repositories, services, task families, S3 prefixes, and distribution. No database credentials or Secrets Manager read permissions are granted to the pipeline. If these resources use customer-managed KMS keys, add the required permissions scoped to those keys.
6. In **GitHub → production environment → Environment variables**, set `AWS_DEPLOY_ROLE_ARN` to the new role ARN. Do not add AWS access keys or application secrets to GitHub. Existing application secrets remain in ECS references and are retrieved by task execution roles.
7. Merge the workflows into the team repository's `main` branch, confirm CI succeeds, then trigger the first deployment. If IAM provider/role creation is denied by school organization policies, stop here and ask the school administrator to provision them.

Only the frontend and deployment metadata prefixes are writable by the role. Supplier image keys at the bucket root and under `suppliers/` are outside its object permissions. No object deletion permission is needed. The trust and permissions JSON are setup templates, not resources already installed in AWS.

Sources: [GitHub OIDC with AWS](https://docs.github.com/en/actions/how-tos/secure-your-work/security-harden-deployments/oidc-in-aws), [immutable OIDC subjects](https://docs.github.com/en/actions/reference/security/oidc), [ECR push permissions](https://docs.aws.amazon.com/AmazonECR/latest/userguide/image-push-iam.html), [ECS deployment permissions](https://github.com/aws-actions/amazon-ecs-deploy-task-definition#permissions).

### Deploying a change

Open **team repository → Actions → Deploy → Run workflow**, select branch `main`, and use operation `deploy`.

| Component selection | Behavior |
|---|---|
| `auto` | Deploy only components whose build inputs changed since their own recorded successful deployment |
| A named service or `frontend` | Redeploy just that component, even if its source has not changed |
| `all` | Deploy every component |

Each service owns its folder; frontend owns `user-frontend/`. Supplier also depends on `data/csv/supplier-seed-data.csv` and the root `.dockerignore`. Deployment-script/configuration/deploy-workflow changes conservatively select all components. Markdown-only changes do not trigger deployments. CI still checks all components.

State is stored at `s3://foc-supplier-images/deployments/state/<component>.json`, including the source commit and one previous release. Change detection compares the full Git trees against each component's record, so multiple commits accumulate and deleted files are included. Missing history errors stop the workflow rather than silently skipping changes. The first run has no baseline and selects all components in `auto`; explicitly select one component to bootstrap gradually. All existing migration SQL files are flagged for that component's first deployment, even if already applied.

For a backend, the workflow builds and pushes a uniquely tagged image (`<commit>-<run-id>-<attempt>`), then uses its immutable digest in the new revision. It updates only the selected ECS service, waits for stability while running, and verifies ECS retained the requested revision rather than automatically rolling back. Public checks use the configured JSON endpoints. These checks establish deployment/readiness, not authenticated business-workflow correctness; order readiness can return HTTP 200 while reporting degraded dependencies.

ECS desired counts are never supplied to `update-service`. A service at zero stays stopped and is reported as **updated while stopped; runtime checks skipped**. Its configuration baseline is recorded after the revision is selected; this is not proof the image starts. When you later scale it up, ECS uses that selected revision. Auto Scaling or manual actions can independently change counts; avoid console deployments during a workflow run. GitHub serializes deploy/rollback runs without cancelling a deployment already in progress. Multi-component releases are sequential and not atomic; successful components retain their records if a later component fails.

For the frontend, the workflow builds static content, saves a release snapshot under `deployments/frontend/<release>/`, uploads non-HTML assets before `frontend/index.html`, and invalidates `/` and `/frontend/index.html`. It waits for invalidation completion and verifies the served HTML hash. Uploads never delete objects, so old browser sessions retain their hashed assets and supplier images remain untouched. Old snapshots/assets and ECR images consume storage; cleanup remains manual and must preserve rollback releases. Do not apply a bucket lifecycle policy that deletes `deployments/state/`.

### Manual database migrations

If selected components have changed migration SQL, the workflow lists the files and stops before any build/push/deployment unless **migrations_applied** is checked. This checkbox is the operator's attestation, not a database query or migration runner.

1. Review the exact workflow commit and listed migrations. For a first deployment, compare all listed files against the database's existing Goose versions.
2. From an authorized machine with RDS network access, use each service's documented Goose procedure with its migration credentials: [user](../user-service/migrations/README.md), [supplier](../supplier-service/migrations/README.md), [credit](../credit-service/README.md), and [order](../order-service/migrations/README.md). Check `status`, apply only pending migrations with `up`, and confirm `status` afterward. Use TLS verification and the intended service database; do not reset users or bootstrap existing databases.
3. Keep migrations backward compatible with running application versions. If that is impossible, plan downtime and explicitly stop affected services before applying them. Never assume an application rollback reverses schema or data changes.
4. Trigger Deploy again on the same reviewed commit with **migrations_applied** checked. If `main` advanced, review and apply any additional migrations before confirming. A rerun of the original workflow retains its commit; a fresh dispatch selects the current branch commit.

RDS does not need to be exposed to GitHub runners. A future dedicated ECS migration task can replace the manual procedure, using VPC access and separate credentials, but no such task is implemented here.

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

The script tests exercise accumulated change selection, shared inputs, migration blocking, fork/branch restrictions, task-definition preservation, zero desired counts, ECS automatic rollback detection, safe frontend uploads, failed-release state handling, and recorded rollback. Local checks cannot establish live AWS permissions or replace an initial GitHub deployment trial.

Local verification passed deployment-script tests, actionlint, frontend type checking/build, and OpenAPI validation (with existing warnings). Supplier, credit, and order tests passed with disposable PostgreSQL, RabbitMQ, and Redis dependencies. Enabling user-service's database tests exposed two outdated assertions, which have now been corrected. After the user confirmed soft deletion is intentional, `TestPostgresServiceFlow` was updated to check that unconfirmed deletion leaves the account accessible, confirmed deletion retains an inactive row with a deletion timestamp and unchanged username/email, and profile reads then return not-found. `TestBootstrapConflictAndRollback` now expects the pre-existing matching account in its subject-conflict case, verifies existing accounts remain unchanged, and still requires bootstrap completion to remain unset. The full user-service suite, vet, and build pass with disposable PostgreSQL as of 6 November. No application behavior was changed. Live AWS permissions and the first GitHub deployment remain unverified.
