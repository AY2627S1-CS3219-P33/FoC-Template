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
