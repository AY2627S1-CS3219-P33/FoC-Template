# ECS deployment notes

Updated 18 October 2026. This records the AWS setup and troubleshooting discussed for the FoC services. It distinguishes observed settings from items still to verify; it is not an infrastructure-as-code source of truth.

## Current setup

- AWS Region: `ap-southeast-1`; ECS cluster: `comfortable-squirrel-pxwxt5`.
- Services discussed: `user-frontend`, `user-service`, and `supplier-service`.
- The user-service was moved to Fargate and reported as running after its PostgreSQL access was corrected. Its task was given a public IP to reach Auth0.
- Supplier-service remained on its existing ECS compute when its Auth0 JWKS requests began failing. The exact current launch type and subnet should be checked in ECS before changing it.
- The frontend is served by Nginx on container port 80. Its ALB target group is IP type, HTTP port 80; the target was shown healthy.
- Task definitions and images were reported as ARM64. Verify each active task definition's runtime architecture against the image before rebuilding or changing compute.

## Networking and service discovery

- Frontend Nginx routes `/api/` to `user-service:8080` and `/supplier-service/` to `supplier-service:3002` (`user-frontend/nginx.conf`).
- ECS Service Connect is enabled in namespace `foc-local`. Screenshots showed endpoints `user-service:8080` and `supplier-service:3002`; supplier uses port name `supplier-service-port`.
- The frontend security group was shown allowing inbound TCP 80 from `alb-public-sg` and allowing all outbound traffic.
- The supplier task's security group was shown allowing TCP 3002 from `frontend-service-sg`; port 8080 was also allowed from that source for user-service. These rules are appropriate only if those are the groups attached to the running task ENIs.
- The RDS access pattern discussed is a PostgreSQL inbound rule on the RDS security group, TCP 5432, sourced from each service task security group. Keep database access restricted to application security groups rather than public CIDRs.

## Supplier seed CSV packaging

The supplier image is intended to bundle the seed file:

- `supplier-service/Dockerfile` copies `data/csv/supplier-seed-data.csv` into the image at `/app/data/supplier-seed-data.csv` and sets `SUPPLIER_SEED_CSV_PATH` to that path.
- Root `.dockerignore` allows the supplier source and seed CSV into the build context.
- `compose.yaml` and `supplier-service/README.md` were updated for the repository-root build context and bundled file.
- Build from the repository root, not from `supplier-service/`:

  ```sh
  docker build --platform linux/arm64 \
    -f supplier-service/Dockerfile \
    -t supplier-service:seed-check .
  ```

The task definition was later shown with `SUPPLIER_SEED_CSV_PATH=/app/data/supplier-seed-data.csv`, but a startup log still reported that the configured CSV could not be opened. A rebuilt image with the file verified inside it and a new task definition/image deployment were recommended. The later API response shows the supplier app was reachable, but no explicit successful seed-import log was provided here; treat the seed fix as not fully verified.

## PostgreSQL

- The shared RDS PostgreSQL instance has separate databases for services. User-service and supplier-service Goose migrations were previously reported applied at versions 5 and 2 respectively.
- The user-service was reported up after creating separate task/database security-group access.
- Supplier's current 503 is not the earlier PostgreSQL connection error: it is an authentication dependency response. The supplier endpoint is reachable, so prioritize Auth0 egress diagnostics before changing database rules.
- See [`rds-goose-migrations.md`](rds-goose-migrations.md) for the RDS connection and migration procedure.

## Auth0 failure identified in supplier-service

An API request to `/supplier-service/suppliers` returned HTTP 503 with:

```json
{"code":"DEPENDENCY_UNAVAILABLE","message":"authentication service unavailable"}
```

Supplier CloudWatch logs then showed `auth.jwks_refresh` with `outcome=unavailable`, about 3 seconds per attempt, and later `circuit_open=true`. The supplier authentication middleware maps an unavailable Auth0 signing-key lookup to that 503. This is strong evidence that supplier-service cannot fetch Auth0's JWKS over HTTPS; it is not evidence of a frontend-to-supplier security-group failure.

The user-service's Fargate public-IP change gives that service its own outbound path. No NAT Gateway was created. Supplier-service needs its own outbound HTTPS access to the configured Auth0 issuer:

- **Quick development option:** run supplier-service on Fargate in a public subnet with public IP assignment enabled. Keep inbound rules restricted to the frontend task security group and port 3002. Fargate requires `awsvpc`, compatible task CPU/memory, and a matching image architecture.
- **Future private-subnet option:** keep tasks private and route outbound traffic through a NAT Gateway. The earlier route-table screenshots showed default routes to an Internet Gateway, not a NAT Gateway. For EC2 tasks using `awsvpc`, the task ENI does not receive a public IP; an IGW route alone does not provide task internet access.

After enabling egress, check CloudWatch for `auth.jwks_refresh` with `outcome=success`, then retry the API. Do not log or share bearer tokens.

## Cost pause discussed

Pausing resources was discussed but not confirmed as completed:

- Set ECS service desired counts to zero to stop application tasks; restore their previous counts to resume.
- Check whether ECS Managed Instances / EC2 capacity actually scales in. Running instances may continue to incur charges even with no service tasks.
- RDS can be stopped temporarily, but storage/backups remain billable and RDS automatically restarts a stopped DB instance after seven days. Stopping the shared DB makes all services using it unavailable.
- An existing ALB continues to incur hourly charges while it exists. Keeping it makes resuming easier; deleting it requires recreating its listeners, target groups, and service attachment.

## Not confirmed

- The current supplier task's ENI, subnet route, and exact compute type were not inspected after the JWKS failure.
- No successful `auth.jwks_refresh` entry was shared.
- No explicit successful supplier seed-import log was shared after the configured CSV path was corrected.
- Whether any resources were paused remains unconfirmed. A NAT Gateway is a future consideration, not part of the current setup.
