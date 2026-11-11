# CS3219 — Software Design and Architecture (AY2627 Sem 1)

## Friend on Campus (FoC)

**Friend on Campus (FoC)** is a peer-to-peer campus errand platform. Students
who need items collected from stores, facilities or landmarks on campus act as
**requesters** and post errand requests. Other students act as **couriers**,
accept open requests, pick up the items and deliver them.

The platform runs on a **closed credit economy**: credits cannot be bought,
withdrawn or exchanged for money, and only circulate within the platform.
Every student receives an initial credit allocation on sign-up. When a
request is created, its credits are reserved from the requester's balance;
when the delivery is confirmed, they are transferred to the courier. If the
request is cancelled or expires before being accepted, the reserved credits
are released.

---

## Team Members

**Team 33**

| Name | Role |
| ----- | ----- |
| Brendan | User Service, Cloud Deployment, CI/CD |
| Gabriel | Order Service, Supplier Service, Requester Enhancements |
| Lucas | Credit Service, Admin Service |
| Hung | Supplier Service, Order Service, Chat Service |
| Zi Yang | Frontend, Notification Service |

---

## Repository Structure

The repository follows a **one-service-per-folder** structure. Each
microservice lives in its own top-level folder with its own `Dockerfile`,
`README.md`, migrations and (where applicable) `openapi.yaml`.

```text
.
├── user-service/           # Registration, authentication (Auth0), profiles, requester/courier/admin roles
├── supplier-service/       # CRUD, search and filtering of campus stores, facilities and landmarks
├── order-service/          # Errand lifecycle: create, list, accept, pick up, complete, cancel, expire
├── credit-service/         # Closed credit economy: initial allocation, reserve, transfer, release
├── chat-service/           # Real-time chat between requester and courier (N2H)
├── notification-service/   # Real-time status update notifications to users (N2H)
├── admin-service/          # Admin dashboard metrics read model built from order events (N2H)
├── queue-service/          # RabbitMQ message broker configuration for async/event-driven workflows
├── user-frontend/          # Responsive React + TypeScript (Vite) web client, served by nginx
├── data/                   # Seed data for suppliers (CSV files and images)
├── docs/                   # Event storming diagram, AsyncAPI spec, deployment and migration notes
├── .github/workflows/      # CI (build/test) and deployment workflows
├── compose.yaml            # Docker Compose stack for running the system locally
├── .env.example            # Template for required environment variables
├── AGENTS.md               # Instructions for AI coding agents (one per service folder as well)
└── README.md
```

See each service's own `README.md` for its API, data model and how to run it.

---

## Local Compose stack

Copy `.env.example` to `.env`, set the Auth0 values, then run
`docker compose up --build`. Open the frontend at <http://localhost:8081>.

The root [compose.yaml](compose.yaml) defines the user, supplier, credit and
notification services, the frontend and RabbitMQ, and pulls in the order, chat
and admin stacks from their own `compose.yaml` files via `include`. The whole
system therefore starts with a single command.

| Service | Host port | Supporting containers |
| ----- | ----- | ----- |
| `user-frontend` (nginx) | 8081 | — |
| `user-service` | internal only | `user-db`, `user-migrate` |
| `supplier-service` | internal only | `supplier-db`, `supplier-migrate` |
| `order-service` | 3003 | `order-db`, `order-redis`, `order-migrate` |
| `credit-service` | 3004 | `credit-db`, `credit-migrate` |
| `admin-service` | 3005 | `admin-db`, `admin-migrate` |
| `chat-service` | 3006 | `chat-db`, `chat-migrate` |
| `notification-service` | 3007 | `notification-db`, `notification-migrate` |
| `queue-service` (RabbitMQ) | 5672 (AMQP), 15672 (management UI) | — |

All host ports are bound to `127.0.0.1`. The frontend's nginx proxies API
calls to the services over the Compose network. RabbitMQ credentials default to
`foc` / `foc-local-dev` and can be overridden with `RABBITMQ_DEFAULT_USER` and
`RABBITMQ_DEFAULT_PASS`. The management UI is at <http://localhost:15672>.

Each service has its own database in a separate named volume. `docker compose
down` keeps their data; `docker compose down -v` deletes it. A Goose migration
job (`<service>-migrate`) runs before each API starts, and Goose applies only
migrations not yet recorded in that database. To add a migration, add the next
numbered `.sql` file with `-- +goose Up` and `-- +goose Down` sections to the
service's `migrations/` folder, then run
`docker compose run --rm <service>-migrate` (e.g. `user-migrate`,
`order-migrate`, `credit-migrate`).
See each service's migration README for its history and rollback details.
If reusing a user database where versions 1–4 were applied before Goose was
introduced, follow the existing-database baseline instructions in the user
migration README before starting the stack; do not baseline a fresh database.

### Admin dashboard

The admin area (`/admin`) has an **Overview** tab and a **Metrics** tab. The
Overview counters (open reviews, ongoing errands, errands waiting for a
courier) are live reads from order-service (`GET /admin/overview`, permission
`errands:review`). The Metrics tab and the readiness section of Overview come
from `admin-service/`, which builds a read model from order-service's events
and needs the Auth0 permission `dashboard:read`. See
[admin-service/README.md](admin-service/README.md) and, for the AWS and Auth0
steps that are not yet done, [docs/ecs-deployment-notes.md](docs/ecs-deployment-notes.md).

---

## AI Use Summary

**Tools:** Claude Opus 5.5, GPT 6.1 Sol

**Prohibited phases avoided:** requirements elicitation and prioritisation;
architecture and design decisions (service boundaries, design patterns, data
schemas, interfaces, and performance/security trade-offs); decision rationales.
All of these were produced by the team.

**Used for:**

- **Scaffolding** — generating boilerplate such as project layout, Dockerfiles,
  Docker Compose and CI configuration, and repetitive glue code.
- **Generating tests** — suggesting and writing unit and integration test cases,
  including edge cases, which the team then reviewed and extended.
- **Implementation** — writing implementation code once the requirements,
  architecture and design were finalised by the team. The AI was given the
  team's requirements, architecture and design (e.g. service boundaries, data
  schemas, API specifications and event schemas) as input, and implemented code
  that conforms to them. It did not propose or change any of these.

**Verification:** All AI outputs were reviewed, edited and tested by the
authors. Each member remains accountable for understanding the code they
contributed.
