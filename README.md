# CS3219 — Software Design and Architecture (AY2627 Sem 1)

## Friend on Campus (FoC)

**Friend on Campus (FoC)** is a peer-to-peer campus errand platform where
students can request items to be collected from stores or facilities on
campus, and other students can fulfil (and deliver) those requests. The
platform runs on a closed credit economy — credits cannot be bought,
withdrawn, or exchanged for money, and only circulate within the platform.

---

## Team Members

| Name | Role |
| ----- | ----- |
| Your Name | Your ownership |
| Your Name | Your ownership |
| Your Name | Your ownership |
| Your Name | Your ownership |
| Your Name | Your ownership |

---

## Repository Structure

This repository follows a **one-service-per-folder** structure: each
microservice (`user-service/`, `supplier-service/`, `order-service/`,
`credit-service/`) lives in its own top-level folder.

```text
.
├── user-service/
├── supplier-service/
├── order-service/
├── credit-service/
├── <n2h-service>/
└── README.md
```

- Any **nice-to-have (N2H)** feature that warrants its own service should
  be added as an **additional folder** at the same level, following the
  same per-service structure.
- Files for agentic coding tools (e.g. agent configs, prompts, skills)
  may be added as needed, but must still **respect the
  one-service-per-folder skeleton** for core implementation.

## Local Compose stack

Copy `.env.example` to `.env`, set the Auth0 values, then run
`docker compose up --build`. Open the frontend at <http://localhost:8081>.
RabbitMQ is intentionally not included yet.

The user and supplier databases have separate named volumes. `docker compose
down` keeps their data; `docker compose down -v` deletes it. Goose migration jobs
run before each API starts, and Goose applies only migrations not yet recorded
in that database. To add a migration, add the next numbered `.sql` file with
`-- +goose Up` and `-- +goose Down` sections, then run
`docker compose run --rm user-migrate` or `docker compose run --rm supplier-migrate`.
See each service's migration README for its history and rollback details.
If reusing a user database where versions 1–4 were applied before Goose was
introduced, follow the existing-database baseline instructions in the user
migration README before starting the stack; do not baseline a fresh database.

Order and credit are not included because their service folders do not yet
contain runnable implementations.

---
