# Admin service

admin-service is the read side of the admin dashboard. It keeps a small
**read model** built from order-service's errand events, computes the
dashboard's metrics from it at query time, and reads every service's `/readyz`
for the readiness section. The live counters on the Overview tab (open
reviews, ongoing errands, errands waiting for a courier) are not here: they
are `GET /admin/overview` in order-service.

The HTTP contract is [openapi.yaml](openapi.yaml). Every route needs a
**login** token (not a machine token) for the audience
`https://api.foc.local/admin-service` with the permission `dashboard:read`,
and every response carries `asOf`.

## How it works

```text
order-service ──errand.# events──▶ admin-service.events queue ──▶ consumer ──▶ transition_facts
      │                                                                          errand_facts
      └── /internal/export/* ◀── enrichment worker (suppliers, buildings)         │
                                 backfill job (history)                           ▼
                                                                  /v1/metrics/* (computed on read)
```

- **Consumer.** Queue `admin-service.events` (quorum, delivery limit 5,
  dead-letter queue `admin-service.events.dlq`) bound to `errand.#` on
  `foc.events`. Binding it changes nothing for other consumers. Events are
  deduplicated by `messageId`, merged into `errand_facts` only when their
  `errandVersion` is at least the stored one (equal versions, the
  `withdrawn`/`expired` pair, are ordered by `occurredAt`, a terminal event
  winning an exact tie), and acknowledged only after the commit. A crash
  between commit and ack redelivers a message that then changes nothing. An
  undecodable message is rejected without requeue so it dead-letters. Rejection
  events and reminders share the binding and are acknowledged and ignored.
- **Enrichment.** The events carry no supplier or building. After an
  `errand.accepted` and after every terminal event, the enrichment worker asks
  order-service's export for them by errand ID and stores `supplier_id`,
  `supplier_name`, `delivery_building_id` and `delivery_building_name`. When
  order-service is down, or supplier-service was down for it so the supplier
  name is null, the errand stays due and is retried (after 5 minutes for a null
  name) without blocking the event stream.
- **Backfill.** [cmd/backfill](cmd/backfill/main.go) pages through
  `GET /internal/export/transitions` and records each event exactly as the
  consumer does. See [Backfill](#backfill).
- **Metrics.** Computed from the two fact tables when asked. There are no
  rollup tables: measured on 200,000 errands and 800,000 events, every query
  takes 4 to 10 ms for a 30-day period and 25 to 55 ms for the 366-day
  maximum. Add rollups only if a query becomes measurably slow.

## Metrics

Days are counted in `Asia/Singapore` (`DASHBOARD_TIMEZONE`). A period is whole
days, both ends included, at most 366 days, default the last 30.

| Route | Definition |
| --- | --- |
| `GET /v1/metrics/errands-per-day` | Errands per day, by the time the errand **opened** (the `errand.opened` event, `PENDING_RESERVATION → OPEN`), not `errands.created_at`, which includes the credit-reservation wait. Every day is listed |
| `GET /v1/metrics/time-to-accept` | Median and p90 of first accept minus opened, for errands whose first accept falls in the period. A later withdraw and re-accept does not change it; never-accepted errands are excluded |
| `GET /v1/metrics/outcomes` | Errands that **finished** (reached a terminal state) in the period, over all five: `COMPLETED`, `CANCELLED`, `EXPIRED`, `REJECTED`, `REFUNDED`. All five are always listed, so the rates add to 1 |
| `GET /v1/metrics/top-suppliers` | Pickup suppliers ranked by errands finished in the period (then by completed), with the completed count beside it |
| `GET /v1/metrics/top-buildings` | The same for delivery buildings |
| `GET /v1/metrics/active-couriers` | Distinct couriers with at least one accept in the period |
| `GET /v1/metrics/reward-volume` | The sum of `reward` on `COMPLETED` errands, by the day they completed |
| `GET /v1/readiness` | Each service's `/readyz`, read live |

Rankings leave out finished errands whose supplier or building has not been
enriched yet, and say how many in `unattributed`.

## Readiness

`GET /v1/readiness` calls `/readyz` of every configured service, all at once,
over the internal network (Service Connect in AWS): order-service,
supplier-service, credit-service, user-service and admin-service itself. A
service is `up` when it answers 200 (`ready` or `degraded`) and `down`
otherwise, with the reason and whatever dependency detail it reports. Only
order-service and admin-service report detail; supplier-service, credit-service
and user-service say just `ready` or `not_ready`, and none is invented for them.

A service whose URL is not configured is returned under `notChecked` with that
reason. user-service's `/readyz` (`GET /readyz`, 200 `{"status":"ready"}` or 503
`{"status":"not_ready"}`) checks that its database answers; its older
`GET /health` only says the process does.

## Auth0 (set up by a person)

| API | Grant | Permission |
| --- | --- | --- |
| New API, identifier `https://api.foc.local/admin-service` | Administrator role | `dashboard:read` |
| order-service API | The admin-service machine-to-machine application | `orders:admin-read` (define it on the API first) |

The machine-to-machine application needs no other API. Order-service
authorises `orders:admin-read` separately from `orders:internal`.

## Setup

Install Go 1.27, then from `admin-service/`:

```sh
go mod download
go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate
```

The sqlc package `internal/database/admingen` is excluded from Git, so run
`generate` after a fresh checkout and whenever `migrations/` or `queries/`
change. The build fails without it.

## Database and migrations

Two roles, never one: a **migrator** that owns the schema and runs the
migrations, and the **runtime** role `admin_service`, which can only read and
write rows. See [migrations/README.md](migrations/README.md). Locally,
`docker compose up` creates both (`db-init/`) and runs the migrations.

Tables: `transition_facts` (one row per event, primary key `message_id`) and
`errand_facts` (one row per errand: latest status and version, `opened_at`,
`first_accepted_at`, the terminal status and time, the supplier and building,
and the enrichment bookkeeping).

## Configuration

`internal/config` reads every variable at startup and reports all problems at
once, without echoing values. [.env.example](.env.example) lists them.

| Variable | Default | Notes |
| --- | --- | --- |
| `HTTP_HOST`, `HTTP_PORT` | `0.0.0.0`, `3005` | |
| `DATABASE_URL` | none | The **runtime** role. `TEST_DATABASE_URL` when `APP_ENV=test` |
| `AUTH0_ISSUER`, `AUTH0_AUDIENCE` | none, `https://api.foc.local/admin-service` | |
| `ADMIN_M2M_CLIENT_ID`, `ADMIN_M2M_CLIENT_SECRET` | none | The machine-to-machine application with `orders:admin-read` |
| `ORDER_AUDIENCE`, `ORDER_SERVICE_URL`, `ORDER_SERVICE_TIMEOUT` | `https://api.foc.local/order-service`, none, `5s` | |
| `AMQP_URL`, `EVENTS_PREFETCH` | none, `16` | |
| `DASHBOARD_TIMEZONE` | `Asia/Singapore` | The zone whose midnights bound a day |
| `SUPPLIER_SERVICE_URL`, `CREDIT_SERVICE_URL`, `USER_SERVICE_URL`, `READINESS_TIMEOUT` | empty, empty, empty, `2s` | A service with no URL is listed as not checked |

## Running

Under the root Compose stack (`docker compose up --build` from the repository
root) the service is on <http://localhost:3005>. On the host, with a `.env`
copied from `.env.example`:

```sh
go run ./cmd/admin-service
```

## Backfill

Loads event history from order-service. Run it **once, after the service has
started**, so its queue is bound: the export pages by outbox ID, which can
differ from commit order for rows written at the same moment, and a row a page
skipped then also arrives on the stream. Run it with the service's own
environment:

```sh
go run ./cmd/backfill            # on the host
/app/admin-backfill              # in the image, as a one-off task
```

It is idempotent: it records events by `messageId` through the same path as the
consumer, so running it twice leaves both tables identical, and it can run
while the service is live. It does not enrich errands; the service's
enrichment worker does that for the history afterwards. It exits non-zero if
the export held events the read model refused (they are logged).

## Tests

```sh
docker compose -f ../order-service/compose.test.yaml up -d --wait
export TEST_DATABASE_URL='postgres://order_test@127.0.0.1:5434/order_test?sslmode=disable'
export TEST_AMQP_URL='amqp://guest:guest@127.0.0.1:5673/'
go test -p 1 ./...
```

Use `-p 1`: the integration tests share one broker and the fixed queue
`admin-service.events`, as CI does. Without the two variables the database and
broker tests skip.

## Deployment

The image is built from this directory (`Dockerfile`) and holds
`/app/admin-service` and `/app/admin-backfill`. The AWS resources and the
`deploy.json` entry are a checklist in
[docs/ecs-deployment-notes.md](../docs/ecs-deployment-notes.md); none exists
yet.
