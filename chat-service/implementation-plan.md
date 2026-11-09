# Chat service implementation plan

Status: phase 1 (skeleton) implemented; phases 2 to 10 not started. Last updated 9 November 2026.

This plan describes a new Go service, `chat-service`, that lets the **requester** and the
**assigned courier** of an errand message each other in real time. It follows the same
conventions as `order-service` and `credit-service` (Go 1.27, `net/http` `ServeMux`,
PostgreSQL with goose and sqlc, Auth0 JWTs, RabbitMQ via `amqp091-go`), so code and habits
carry over from those services.

The plan is grouped by component. Each section says what the component is for, what to build
and how to check it works. [Section 15](#15-build-order) gives the order to build them in.

---

## 1. Requirements and decisions

### Requirements

| ID | Requirement |
|---|---|
| CH-1 | A chat room is created when a courier accepts an errand. |
| CH-2 | The room connects the errand's **requester** and **courier**. The errand's contact person is not a participant. |
| CH-3 | Participants exchange messages in real time and can see earlier messages after a refresh or reconnect. |
| CH-4 | When the errand ends for a room, the room stays open for **24 hours** and then closes. The errand ends when it is completed or refunded, or, for that courier's room, when the courier withdraws. |
| CH-5 | Rooms and their messages stay in the database after closing. Nothing is deleted. |
| CH-6 | An administrator can extract (download) the chat log of any room, open or closed. |
| CH-7 | The service runs as one copy now, and can later run as several copies with Redis without restructuring. |

### Decisions

| Topic | Decision | Reason |
|---|---|---|
| What "ends" a room | `errand.completed` or `errand.refunded` end every room of the errand. `errand.withdrawn` ends only the withdrawing courier's room. Delivery (`DELIVERED`) and disputes (`REVIEW_REQUIRED`) do **not** end it. | The requester may need the courier while verifying a delivery or during a dispute. |
| Grace period | The room closes 24 hours after the end time. The end time is the event's `occurredAt` (when the errand changed), not when chat-service processed the event. The delay is configurable (`CHAT_CLOSE_DELAY`). | Both people get time to wrap up ("left it with the guard"). Using `occurredAt` gives the same result even if the event is processed late. A short delay (e.g. 1 minute) makes testing easy. |
| What "closed" means | **Read-only.** Participants can still see the room and its history, but cannot send. | They can look back at what was agreed. Admins can export it (CH-6). |
| How a room closes | No background job. Each room stores `closes_at`, and the server compares it with the current time whenever someone sends a message or reads the room. | A time comparison cannot be missed, needs no scheduler, and behaves the same on every copy of the service. |
| One room per assignment | A room belongs to one `(errandId, courierId)` pair. If courier A withdraws and courier B accepts, B gets a **new, empty** room and cannot see A's conversation. A's room still gets its 24 hours. | Privacy: B was not part of that conversation. Order-service already forbids a courier from re-accepting an errand they withdrew from (F3.6.3), so the pair is unique and a closed room never needs to reopen. |
| Who can export | Administrators only, with a new `chats:export` permission. | Chosen by the product owner. |
| Retention | Kept indefinitely for now. | Chosen by the product owner. See [section 16](#16-open-questions-and-later-work). |
| How a message reaches the other person | Over a WebSocket. A REST endpoint can also send messages; it is used for testing and as a fallback. | Real-time delivery needs the server to push. |
| Live delivery across copies | Through a `Broadcaster` interface. `LocalBroadcaster` now, `RedisBroadcaster` later. | CH-7. See [section 9](#9-broadcast-live-delivery-local-now-redis-later). |

### Room states

A room's state is **calculated**, not stored:

```
                errand.accepted
                       │
                       ▼
                ┌─────────────┐   errand.completed / refunded,
                │    OPEN     │   or errand.withdrawn (this courier)
                │ closes_at = │──────────────────────────────┐
                │    NULL     │                              │
                └─────────────┘                              ▼
                                                   ┌──────────────────┐
                                                   │     CLOSING      │
                                                   │ now < closes_at  │  can still send
                                                   └────────┬─────────┘
                                                            │ 24 h pass
                                                            ▼
                                                   ┌──────────────────┐
                                                   │     CLOSED       │
                                                   │ now >= closes_at │  read-only
                                                   └──────────────────┘
```

```go
func (r Room) Status(now time.Time) RoomStatus {
    switch {
    case r.ClosesAt == nil:     return StatusOpen
    case now.Before(*r.ClosesAt): return StatusClosing
    default:                    return StatusClosed
    }
}
```

### Out of scope for the first version

Typing indicators, "online" status, read receipts, images or files, editing or deleting
messages, and push or email notifications for unread messages. Each can be added later
without changing the core design.

---

## 2. Big picture

```
 Requester browser                                   Courier browser
 ┌───────────────┐                                   ┌───────────────┐
 │  Chat panel   │                                   │  Chat panel   │
 └──────┬────────┘                                   └───────┬───────┘
        │ REST:  /chat-service/...   (history, lists)        │
        │ WS:    /chat-service/ws    (live messages)         │
        ▼                                                    ▼
 ┌──────────────────────────────────────────────────────────────────┐
 │ nginx (local)  /  CloudFront → ALB (AWS)                         │
 │ /chat-service/* → chat-service:3005, prefix stripped             │
 └─────────────────────────────┬────────────────────────────────────┘
                               ▼
 ┌──────────────────────────────────────────────────────────────────┐
 │ chat-service (one Go process)                                    │
 │                                                                  │
 │  httpapi ─┐                                                      │
 │           ├──► chat (rules) ──► store ──────────────► chat-db    │
 │  ws ──────┘         │                                (Postgres)  │
 │   ▲                 ▼                                    ▲       │
 │   └──── hub ◄── broadcast (Local now / Redis later)      │       │
 │                                                          │       │
 │  events (RabbitMQ consumer) ──► chat (create/end rooms) ─┘       │
 └──────────────────────────────▲───────────────────────────────────┘
                                │ errand.accepted / withdrawn / completed / refunded
                        ┌───────┴────────┐        ┌───────────────┐
                        │ RabbitMQ       │◄───────│ order-service │
                        │ foc.events     │        │ (unchanged)   │
                        └────────────────┘        └───────────────┘
```

**Rule of thumb:** PostgreSQL holds everything that must be correct. Memory holds only the
open WebSocket connections. This rule is what makes scaling with Redis a small change later.

---

## 3. Folder layout

```text
chat-service/
├── cmd/chat-service/main.go        # Entry point: calls app.Run
├── internal/
│   ├── app/                        # Composition root: builds and wires every component
│   ├── config/                     # Environment variables
│   ├── auth/                       # Auth0 JWT validation, Principal, permissions
│   ├── httpapi/                    # REST routes, JSON errors, /readyz
│   ├── ws/                         # One WebSocket connection: handshake, read/write loops
│   ├── hub/                        # In-memory map: user → open connections
│   ├── broadcast/                  # Broadcaster interface + Local (+ Redis later)
│   ├── chat/                       # Business rules: who may read/send, room lifecycle
│   ├── store/                      # Repository over sqlc-generated code
│   ├── database/chatgen/           # sqlc output (generated, git-ignored)
│   ├── events/                     # RabbitMQ consumer for errand events
│   ├── readiness/                  # Postgres + RabbitMQ health
│   └── logging/                    # slog setup
├── migrations/                     # goose SQL migrations
├── queries/chat.sql                # SQL for sqlc
├── openapi.yaml                    # REST contract
├── docs/websocket-protocol.md      # WebSocket message contract
├── sqlc.yaml
├── compose.yaml                    # chat-db, chat-migrate, chat-service
├── compose.test.yaml               # Test Postgres + RabbitMQ
├── Dockerfile
├── .env.example
├── AGENTS.md
└── README.md
```

These are packages inside **one program**, not separate services. Calls between them are
ordinary Go function calls, and the service ships as one container.

**Dependency direction** (an arrow means "calls"):
`httpapi`, `ws` and `events` → `chat` → `store` and `broadcast` → `hub`.
`chat` never imports `httpapi` or `ws`, so the rules can be tested without a network.

---

## 4. chat-db (PostgreSQL schema)

**Purpose:** the permanent record of rooms, messages, errand endings and exports.

**Local:** a `chat-db` container (`postgres:17-alpine`), database and user `chat_service`.
**AWS:** a new database `chat_service_db` on the shared RDS instance `foc-shared-database`,
with its own runtime role. The service never runs migrations itself (same rule as credit-service).

### Migration `00001_create_chat_schema.sql`

```sql
-- +goose Up

-- One room per (errand, courier) assignment. Rooms are never deleted.
CREATE TABLE chat_rooms (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    errand_id       uuid        NOT NULL,
    requester_id    text        NOT NULL,       -- Auth0 subject, e.g. auth0|abc
    courier_id      text        NOT NULL,
    -- errandVersion of errand.accepted. NULL only while errand.withdrawn has
    -- arrived before its errand.accepted (RabbitMQ may reorder).
    opened_version  integer,
    -- Set when the errand ends for this room; all three together or none.
    ended_at        timestamptz,                -- the event's occurredAt
    end_reason      text,
    closes_at       timestamptz,                -- ended_at + CHAT_CLOSE_DELAY
    created_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chat_rooms_end_reason CHECK (
        end_reason IS NULL OR end_reason IN ('WITHDRAWN', 'COMPLETED', 'REFUNDED')),
    CONSTRAINT chat_rooms_end_fields CHECK (
        (ended_at IS NULL     AND end_reason IS NULL     AND closes_at IS NULL)
     OR (ended_at IS NOT NULL AND end_reason IS NOT NULL AND closes_at >= ended_at)),
    CONSTRAINT chat_rooms_distinct_people CHECK (requester_id <> courier_id),
    -- Also makes a repeated errand.accepted harmless (see section 11).
    CONSTRAINT chat_rooms_one_per_assignment UNIQUE (errand_id, courier_id)
);
CREATE INDEX chat_rooms_by_requester ON chat_rooms (requester_id, created_at DESC);
CREATE INDEX chat_rooms_by_courier   ON chat_rooms (courier_id, created_at DESC);

CREATE TABLE chat_messages (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,  -- increasing: used for paging
    room_id        uuid        NOT NULL REFERENCES chat_rooms (id),
    sender_id      text        NOT NULL,
    body           text        NOT NULL,
    -- Generated by the browser per message; makes retries safe.
    client_msg_id  text        NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chat_messages_body_length CHECK (char_length(body) BETWEEN 1 AND 2000),
    CONSTRAINT chat_messages_client_msg_id_length CHECK (char_length(client_msg_id) BETWEEN 1 AND 64),
    CONSTRAINT chat_messages_idempotent UNIQUE (room_id, client_msg_id)
);
CREATE INDEX chat_messages_room_id_idx ON chat_messages (room_id, id);

-- Errands that reached COMPLETED or REFUNDED. If errand.accepted arrives
-- after the ending event (RabbitMQ may reorder), its room is created already ended.
CREATE TABLE errand_endings (
    errand_id  uuid PRIMARY KEY,
    reason     text        NOT NULL CHECK (reason IN ('COMPLETED', 'REFUNDED')),
    ended_at   timestamptz NOT NULL
);

-- Who exported which log, and when (chat logs are private).
CREATE TABLE chat_log_exports (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    room_id       uuid        NOT NULL REFERENCES chat_rooms (id),
    admin_subject text        NOT NULL,
    format        text        NOT NULL CHECK (format IN ('json', 'text')),
    exported_at   timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE chat_log_exports;
DROP TABLE errand_endings;
DROP TABLE chat_messages;
DROP TABLE chat_rooms;
```

### Queries (`queries/chat.sql`, generated by sqlc into `internal/database/chatgen`)

| Query | Used by |
|---|---|
| `LockErrand` (`SELECT pg_advisory_xact_lock(hashtextextended($1::text, 0))`) | events: one errand's events are applied one at a time, even across copies |
| `UpsertRoomOnAccept` (`INSERT ... ON CONFLICT (errand_id, courier_id) DO UPDATE SET opened_version = COALESCE(chat_rooms.opened_version, EXCLUDED.opened_version) RETURNING *, (xmax = 0) AS inserted`) | events (`errand.accepted`) |
| `EnsureRoom` (`INSERT ... ON CONFLICT DO NOTHING`, `opened_version` NULL) | events (`errand.withdrawn` arriving before `errand.accepted`) |
| `EndRoom` / `EndRoomsForErrand` (see below) | events |
| `GetErrandEnding`, `InsertErrandEnding` (`ON CONFLICT DO NOTHING`) | events |
| `GetRoom` | chat |
| `ListRoomsForErrandAndUser` | chat (`GET /errands/{errandId}/chats`) |
| `ListRoomsForUser` | chat (`GET /me/chats`) |
| `InsertMessage` (`ON CONFLICT (room_id, client_msg_id) DO NOTHING RETURNING ...`), `GetMessageByClientID` | chat (send) |
| `ListMessagesAfter`, `ListMessagesBefore` (by `id`, with `LIMIT`) | chat (history, catch-up) |
| `ListRoomsForErrand`, `ListAllMessages` | chat (admin export) |
| `InsertExport` | chat (admin export audit) |

`EndRoom` **keeps the earliest ending**, so applying the same or a later event again changes nothing:

```sql
-- name: EndRoom :one
UPDATE chat_rooms
SET ended_at = @ended_at, end_reason = @end_reason, closes_at = @closes_at
WHERE id = @id AND (ended_at IS NULL OR ended_at > @ended_at)
RETURNING *;          -- no row returned = nothing changed
```

`EndRoomsForErrand` is the same with `WHERE errand_id = @errand_id AND (...)`.

**Done when:** `goose up` and `goose down` both work on an empty database, and the
constraint tests in [section 14](#14-testing) pass.

---

## 5. config

**Purpose:** read every setting from environment variables, check them all at start-up,
and report every problem at once without printing secret values (copy the order-service style).

| Variable | Example (local) | Notes |
|---|---|---|
| `APP_ENV` | `development` | `development`, `test` or `production` |
| `HTTP_HOST`, `HTTP_PORT` | `0.0.0.0`, `3005` | |
| `DATABASE_URL` | `postgresql://chat_service:...@chat-db:5432/chat_service?sslmode=disable` | On RDS: `sslmode=verify-full&sslrootcert=/etc/ssl/certs/rds-global-bundle.pem` |
| `AMQP_URL` | `amqp://foc:...@queue-service:5672/` | Required: rooms cannot be created without it |
| `AUTH0_ISSUER` | `https://login.bestfoc.app/` | |
| `AUTH0_AUDIENCE` | `https://api.foc.local/chat-service` | New Auth0 API, see [section 6](#6-auth) |
| `CHAT_CLOSE_DELAY` | `24h` | How long a room stays open after the errand ends. Use `1m` for manual testing |
| `WS_ALLOWED_ORIGINS` | `localhost:8081,localhost:5173` | Comma-separated host patterns allowed to open a WebSocket. Production: `app.bestfoc.app` |
| `WS_AUTH_TIMEOUT` | `5s` | Close connections that do not authenticate in time |
| `WS_PING_INTERVAL` | `25s` | Must be shorter than the ALB idle timeout (60 s) |
| `WS_MAX_CONNECTIONS_PER_USER` | `5` | Limits resource use from one account |
| `CHAT_MAX_MESSAGE_LENGTH` | `2000` | Matches the database check |
| `CHAT_SEND_RATE`, `CHAT_SEND_BURST` | `5`, `10` | Per-connection token bucket: messages per second, and burst |
| `REDIS_URL` | *(empty)* | Empty means `LocalBroadcaster`. Set later to switch to Redis |

**Done when:** a unit test shows a missing `DATABASE_URL` and a bad `HTTP_PORT` are both
reported in one error, and the error text contains no URL.

---

## 6. auth

**Purpose:** turn an Auth0 access token into a trusted `Principal` (user ID and permissions).
Never accept a user ID or role from the request body.

**Build:**
- Copy `order-service/internal/auth` (JWKS cache, validator, `Principal`, middleware) and
  replace the permissions with:
  - `chats:use`: required for every participant route and for the WebSocket.
  - `chats:export`: required for `/admin/*` routes.
- Reject machine-to-machine tokens on all routes (no internal routes are needed yet).
- Add `VerifyToken(ctx, raw string) (Principal, expiry time.Time, error)` for the WebSocket
  handshake, which receives the token in a message, not in a header.

**Auth0 set-up (dashboard, one-off):**
1. Create an API with identifier `https://api.foc.local/chat-service`.
2. Turn on RBAC and "Add Permissions in the Access Token".
3. Add permissions `chats:use` and `chats:export`.
4. Give `chats:use` to the user role and both permissions to the administrator role
   (the same roles user-service already assigns).

**Done when:** tests cover a valid token, wrong audience, expired token, missing permission
(403), and an M2M token on a user route (401 or 403, matching order-service).

---

## 7. chat (business rules)

**Purpose:** the single place that decides what is allowed. REST, WebSocket and the event
consumer all call this package, so their rules cannot drift apart.

```go
type Service struct {
    store       Store        // interface over the store package
    broadcaster Broadcaster  // interface from the broadcast package
    clock       func() time.Time   // injectable, so tests can jump 24 hours ahead
    closeDelay  time.Duration      // CHAT_CLOSE_DELAY
}

// Participants
func (s *Service) MyRooms(ctx, p Principal) ([]Room, error)
func (s *Service) RoomsForErrand(ctx, p Principal, errandID uuid.UUID) ([]Room, error)
func (s *Service) History(ctx, p Principal, roomID uuid.UUID, page Page) ([]Message, error)
func (s *Service) Send(ctx, p Principal, roomID uuid.UUID, clientMsgID, body string) (Message, error)

// Event consumer
func (s *Service) OnAccepted(ctx, e AcceptedEvent) error
func (s *Service) OnWithdrawn(ctx, e WithdrawnEvent) error
func (s *Service) OnErrandEnded(ctx, e ErrandEndedEvent) error   // completed / refunded

// Admin
func (s *Service) AdminRoomsForErrand(ctx, p Principal, errandID uuid.UUID) ([]Room, error)
func (s *Service) AdminExport(ctx, p Principal, roomID uuid.UUID, format string) (Export, error)
```

Every `Room` returned to a client includes `status` (`OPEN`, `CLOSING` or `CLOSED`, from
`Room.Status(clock())`) and `closesAt`, so the browser can show "chat closes in 5 h".

### Rules

**Reading (participants):**
- Caller must be the room's `requester_id` or `courier_id`. Otherwise return
  `404 room_not_found`, so outsiders cannot even tell whether the room exists.
- Reading is allowed in every state, including `CLOSED`.

**Sending (participants):**
- Caller must be a participant (otherwise `404 room_not_found`).
- Room status must be `OPEN` or `CLOSING`. If it is `CLOSED`, return `409 room_closed`.
- Body is trimmed, must be 1–2000 characters, and contain no control characters other
  than newline and tab. Text is stored as typed. Escaping happens when it is displayed (React does this).

**`Send` steps:**
1. `GetRoom(roomID)`. Check the caller is a participant and the room is not `CLOSED`.
2. `InsertMessage ... ON CONFLICT DO NOTHING`. If it already existed (same `client_msg_id`),
   load and return the existing message without broadcasting again.
3. **After the insert has committed**, `broadcaster.Publish` to both participants. Always save
   before you push, so a message is never shown to someone and then lost.

A message sent in the same instant the room closes may or may not be accepted. That is fine:
24 hours have already passed, and the check uses one clock, the server's.

**Room lifecycle (events):** see [section 11](#11-events-rabbitmq-consumer). Each handler runs
in one transaction that first calls `LockErrand(errandId)`, and publishes to the browsers only
**after** commit:
- a new room: `room_opened` to requester and courier;
- a room whose end time was set or moved earlier: `room_closing` (with `closesAt`) to its two participants.

**Admin export:**
- Requires `chats:export`.
- Returns every message in order, with the room's metadata (including `endedAt`, `endReason`
  and `closesAt`) and who said what (`REQUESTER`/`COURIER`).
- Records a `chat_log_exports` row in the same transaction as the read.

**Errors** (stable codes, same JSON shape as other services:
`{"error": "<code>", "message": "<human text>"}`):
`unauthenticated` (401), `forbidden` (403), `room_not_found` (404), `room_closed` (409),
`invalid_message` (400), `rate_limited` (429), `internal` (500).

**Done when:** unit tests with an in-memory store fake and a fake clock cover every rule above,
including:
- sending works while `CLOSING`, and is refused one second after `closesAt`;
- reading works while `CLOSED`;
- courier B cannot read courier A's room for the same errand;
- the requester of errand X cannot read errand Y's room.

---

## 8. store

**Purpose:** the only package that runs SQL. It wraps the sqlc code, adds transaction
helpers (`WithTx(ctx, func(q *chatgen.Queries) error)`) and converts database rows into
`chat` types.

**Build:**
- `sqlc.yaml` with one target (`queries/chat.sql` → `internal/database/chatgen`, `pgx/v5`,
  `emit_interface: true`, `emit_json_tags: true`, `emit_empty_slices: true`), the same
  settings as order-service.
- `pgxpool` set-up with min/max connections from config.
- Map unique-violation and check-violation errors into `chat` errors. Never return raw
  database errors to callers.

**Done when:** integration tests (against `compose.test.yaml` Postgres, `go test -p 1`) cover
every query in [section 4](#4-chat-db-postgresql-schema), including `EndRoom` keeping the
earliest ending.

---

## 9. broadcast (live delivery: local now, Redis later)

**Purpose:** "deliver this payload to these users, wherever they are connected." This is the
**only** component that changes when moving from one copy to several.

```go
// Envelope is what travels between copies (and, later, through Redis).
type Envelope struct {
    Recipients []string        `json:"recipients"` // Auth0 subjects
    Payload    json.RawMessage `json:"payload"`    // a server→client WebSocket frame
}

type Broadcaster interface {
    Publish(ctx context.Context, env Envelope) error
    // Run blocks until ctx ends. LocalBroadcaster returns immediately;
    // RedisBroadcaster subscribes and forwards to the hub.
    Run(ctx context.Context) error
}
```

**Now: `LocalBroadcaster`.** `Publish` calls `hub.Deliver(recipients, payload)` directly.

**Later: `RedisBroadcaster`** (about 50 lines, built when the ECS desired count goes above 1):
- `Publish`: `PUBLISH chat:deliver <json envelope>`.
- `Run`: `SUBSCRIBE chat:deliver`. For each message, call `hub.Deliver(...)`. Each copy
  delivers only to the connections it holds. Resubscribe with backoff if Redis drops.
- `app` chooses the implementation: `REDIS_URL` set → Redis, otherwise Local.
- Library: `github.com/redis/go-redis/v9` (already used by order-service).
- Losing a Redis message is acceptable, because the message is already in Postgres and
  clients catch up on reconnect (see [section 10](#10-ws-and-hub-websockets)).

### Rules that keep the Redis switch small (follow these from day one)

1. Nothing outside `broadcast` and `hub` knows how delivery works. `chat` only calls `Publish`.
2. No business state in memory: rooms, membership, `closes_at` and messages always come from
   Postgres. Do not cache rooms in a Go map. Closing is a time comparison against the database
   value, so every copy agrees without coordinating.
3. Save, commit, then publish.
4. Recipients are users, not connections, so any copy can deliver.
5. Per-connection limits (rate limit, auth timeout) are fine in memory, because a connection only
   lives on one copy. Per-**user** limits across copies (`WS_MAX_CONNECTIONS_PER_USER`)
   become approximate (per copy) once there are several copies. That is acceptable.

**Done when:** a test with a fake hub shows `LocalBroadcaster.Publish` delivers to exactly the
listed recipients.

---

## 10. ws and hub (WebSockets)

### hub

**Purpose:** remember which connections are open in **this** copy, keyed by **user**.

```go
type Hub struct {
    mu    sync.RWMutex
    users map[string]map[*Client]struct{}   // Auth0 subject → that user's open tabs
}
func (h *Hub) Register(c *Client) (ok bool)  // false if the user is at the connection limit
func (h *Hub) Unregister(c *Client)
func (h *Hub) Deliver(userIDs []string, payload []byte)
```

Earlier discussions keyed the hub by room. Keying by **user** is simpler here, for three reasons:
- A room always has exactly two known people, so `Deliver` just needs their IDs.
- The browser never has to "join" or "leave" rooms.
- New rooms and closing notices reach users automatically (`room_opened`, `room_closing`), even
  if they already had the page open.

`Deliver` must never block on a slow client. Each `Client` has a buffered send channel
(e.g. 32 frames). If it is full, drop that client: close the connection with status 1008, and
the browser reconnects and catches up.

### ws

**Purpose:** run one WebSocket connection from start to finish.

**Library:** `github.com/coder/websocket`.

**Endpoint:** `GET /ws` (browser URL `wss://app.bestfoc.app/chat-service/ws`).

**Connection lifecycle:**
1. **Accept** with `websocket.AcceptOptions{OriginPatterns: cfg.WSAllowedOrigins}`.
   This is required: behind nginx or CloudFront the `Host` header differs from the page's
   origin, so the default same-host check would reject every browser. It also stops other
   websites from opening a chat connection.
2. `conn.SetReadLimit(8 KiB)`.
3. **Authenticate.** The first frame must arrive within `WS_AUTH_TIMEOUT`:
   `{"type":"auth","token":"<Auth0 access token>"}`. Browsers cannot send an
   `Authorization` header on a WebSocket, and tokens must not go in the URL (URLs get logged).
   Verify with `auth.VerifyToken` and require `chats:use`. On failure, close with code 4401.
4. `hub.Register`. If the user is over the limit, close with code 4429.
5. Send `{"type":"ready","userId":"..."}`.
6. Start three goroutines:
   - **read loop:** parse frames and handle `send` via `chat.Service.Send`. Reply with `ack`
     to the sender, or `error` with the `clientMsgId` (for example `room_closed`).
   - **write loop:** drain the client's send channel to the socket, with a write timeout.
   - **heartbeat:** `conn.Ping` every `WS_PING_INTERVAL`. If no pong arrives, close.
7. **Token expiry:** close with code 4401 when the token's `exp` passes. The browser gets a fresh
   token and reconnects.
8. **Cleanup** on any exit: `hub.Unregister`, then close the connection.
9. **Shutdown:** on SIGTERM, close all connections with code 1012 ("service restart") so
   browsers reconnect at once (to another copy, once there are several).

**Rate limit:** a token bucket per connection (`golang.org/x/time/rate`). Excess `send` frames
get `error` with code `rate_limited`.

### WebSocket protocol (write this up fully in `docs/websocket-protocol.md`)

Client → server:

| `type` | Fields | Meaning |
|---|---|---|
| `auth` | `token` | Must be the first frame |
| `send` | `roomId`, `clientMsgId` (UUID made by the browser), `body` | Send a message |

Server → client:

| `type` | Fields | Meaning |
|---|---|---|
| `ready` | `userId` | Authenticated. The client now fetches history over REST to catch up |
| `message` | `message` (id, roomId, senderId, senderRole, body, createdAt) | New message in one of your rooms (sent to both participants, including the sender's other tabs) |
| `ack` | `clientMsgId`, `message` | Your `send` was saved |
| `error` | `code`, `message`, optional `clientMsgId` | A frame was refused |
| `room_opened` | `room` | A courier accepted one of your errands, or you accepted one |
| `room_closing` | `roomId`, `errandId`, `endReason`, `closesAt` | The errand ended. The room becomes read-only at `closesAt` |

There is no "room closed" push. The browser already knows `closesAt` and switches to read-only
at that time on its own timer. If its clock is wrong, the server's `room_closed` error on the
next send corrects it.

Close codes: `1000` normal, `1008` policy (too slow or bad frames), `1012` server restart,
`4401` authentication failed or expired, `4429` too many connections.

**Done when:** an integration test using `httptest.Server` and two WebSocket clients shows:
A sends, B receives `message` and A receives `ack`; a third user gets nothing; a resend with the
same `clientMsgId` produces one database row; no `auth` frame within the timeout closes with 4401;
sending to a `CLOSED` room returns `error` `room_closed`.

---

## 11. events (RabbitMQ consumer)

**Purpose:** keep rooms in step with the errand lifecycle published by order-service. No
changes to order-service are needed: it already publishes these events (see
[docs/asyncapi.yaml](../docs/asyncapi.yaml)).

### Topology (declared by chat-service on every connect, idempotently)

| Item | Value |
|---|---|
| Exchange | `foc.events` (topic, durable), declared with the same arguments as order-service |
| Queue | `chat-service.errand-events`, quorum, `x-delivery-limit: 5`, dead-letters to the DLQ |
| Dead-letter queue | `chat-service.errand-events.dlq`, quorum |
| Bindings | `errand.accepted`, `errand.withdrawn`, `errand.completed`, `errand.refunded` |
| Prefetch | 10 |

Model the consumer on `order-service/internal/creditmsg/consumer.go`: reconnect with backoff,
ack only after the database transaction commits, requeue on temporary failures (database down),
reject without requeue on undecodable or invalid messages (they go to the DLQ), and never log the AMQP URL.

Fields come from the `Envelope` and `ErrandEventData` schemas: `occurredAt`, `errandId`,
`errandVersion`, `data.requesterId` and `data.courierId`. Validate them first. If anything
required is missing or malformed, reject (DLQ).

### Handling each event

Every handler runs in **one transaction** that starts with `LockErrand(errandId)`. Below,
`end = occurredAt` and `closes = occurredAt + CHAT_CLOSE_DELAY`.

**`errand.accepted` (version `v`, courier `C`)**
1. `UpsertRoomOnAccept(errandId, requesterId, C, v)`.
2. If `GetErrandEnding(errandId)` finds an ending (the completion arrived first), `EndRoom`
   with that ending's reason and time.
3. Commit. If the room was newly inserted, publish `room_opened`. If step 2 changed it, also
   publish `room_closing`.

**`errand.withdrawn` (courier `C`)**
1. `EnsureRoom(errandId, requesterId, C)`. This creates the room only if the withdrawal
   arrived before its acceptance. The later `errand.accepted` then fills in `opened_version`
   and does not reopen it.
2. `EndRoom(room, 'WITHDRAWN', end, closes)`.
3. Commit. If step 2 changed the room, publish `room_closing` to requester and `C`.

**`errand.completed` / `errand.refunded`**
1. `InsertErrandEnding(errandId, 'COMPLETED' | 'REFUNDED', end)`.
2. `EndRoomsForErrand(errandId, reason, end, closes)`. Rooms already ended by an earlier
   withdrawal keep their earlier time.
3. Commit. Publish `room_closing` for each room that changed.

### Why this is safe with duplicate and out-of-order events

The contract says delivery is **at least once** (duplicates) and **may reorder**.

| Situation | Why it is handled |
|---|---|
| Same event twice | Every step is idempotent: the upsert does nothing new, and `EndRoom` only moves the end **earlier**, so repeating it changes nothing. No "processed events" table is needed |
| `withdrawn` before `accepted` | `EnsureRoom` creates the room already ended. The late `accepted` cannot reopen it |
| `completed` before `accepted` | The ending is remembered in `errand_endings`, and the late `accepted` applies it |
| `accepted` for courier B before `withdrawn` for courier A | Both rooms exist briefly, each with its own courier. A's room ends when its `withdrawn` arrives. Nothing is shared between them |
| Two copies of chat-service handle two events for one errand at once | `LockErrand` makes the second wait for the first |

**Contract update:** add chat-service as a consumer of the four routing keys in
[docs/asyncapi.yaml](../docs/asyncapi.yaml) (description text and `receive` operations).

**Done when:** integration tests against a real RabbitMQ (`compose.test.yaml`) cover the normal
flow (accepted then completed), a duplicate of each event, withdrawn-before-accepted,
completed-before-accepted, a withdrawal followed by a new courier and then completion (A's
room keeps its earlier end time), and an invalid JSON message ending up in the DLQ.

---

## 12. httpapi (REST)

**Purpose:** ordinary request→response routes. All require an Auth0 user token
(`Authorization: Bearer ...`, audience `https://api.foc.local/chat-service`). Paths follow
order-service's style (no version prefix, `/me/...` for "mine", `/admin/...` for admins).
The browser calls them under `/chat-service/...`, and nginx or the ALB strips the prefix.

| Method and path | Permission | Purpose |
|---|---|---|
| `GET /me/chats` | `chats:use` | All my rooms, newest first, each with `status`, `closesAt` and the last message |
| `GET /errands/{errandId}/chats` | `chats:use` | My rooms for this errand, newest first. A courier gets at most one. A requester gets one per courier who accepted. Empty list until the accept event is processed (usually under a second) |
| `GET /chats/{roomId}/messages?after={id}&limit=50` | `chats:use` | Catch-up: messages newer than `id`, oldest first. Works in every state |
| `GET /chats/{roomId}/messages?before={id}&limit=50` | `chats:use` | Scroll back: messages older than `id` |
| `POST /chats/{roomId}/messages` | `chats:use` | Send without a WebSocket (testing, fallback). Body `{clientMsgId, body}`. Same rules and broadcast as WebSocket send. `409 room_closed` when closed |
| `GET /ws` | `chats:use` (checked in the handshake) | WebSocket upgrade |
| `GET /admin/errands/{errandId}/chats` | `chats:export` | Every room for the errand, including withdrawn couriers' rooms |
| `GET /admin/chats/{roomId}/log?format=json\|text` | `chats:export` | Download the full log. `Content-Disposition: attachment; filename="chat-<errandId>-<roomId>.<ext>"`. Logged in `chat_log_exports` |
| `GET /readyz` | none | `200` when Postgres is reachable and the event consumer is subscribed |

`limit` is 1–100 (default 50). Message lists are always ordered by `id`.

**Text export format** (one message per line, times in `Asia/Singapore`):

```text
Chat log for errand 7b0c...  room 5e1f...
Requester: auth0|abc   Courier: auth0|xyz
Opened: 2026-11-09 14:02   Ended: 2026-11-09 14:40 (COMPLETED)   Closes: 2026-11-10 14:40

[2026-11-09 14:03:12] REQUESTER: I'm at the lobby
[2026-11-09 14:05:40] COURIER: On my way up
```

Write `openapi.yaml` for every route before implementing them, as the other services do.

**Done when:** handler tests cover each route's success, 401, 403, 404 and 409 cases, and the
export sets the download header and records an audit row.

---

## 13. Outside chat-service

### 13.1 Local routing (Docker Compose and Vite)

- **`chat-service/compose.yaml`** (included from the root `compose.yaml`, like order-service):
  `chat-db`, `chat-migrate` (using the shared goose migrator image, with its own tag as
  order-service does), and `chat-service` on port `3005`, published as `127.0.0.1:3005:3005`
  for testing. Put the database on a private `chat-internal` network.
- **[user-frontend/nginx.conf](../user-frontend/nginx.conf):**
  ```nginx
  location /chat-service/ {
      proxy_pass http://chat-service:3005/;
      proxy_http_version 1.1;
      proxy_set_header Upgrade $http_upgrade;
      proxy_set_header Connection "upgrade";
      proxy_read_timeout 3600s;
  }
  ```
- **[user-frontend/vite.config.ts](../user-frontend/vite.config.ts):** add a `/chat-service`
  proxy to `http://localhost:3005` with the same prefix rewrite and `ws: true`.
- Add `chat-service` to `user-frontend`'s `depends_on` in the root compose file.

### 13.2 AWS deployment (follow [docs/ecs-deployment-notes.md](../docs/ecs-deployment-notes.md))

1. **RDS:** create database `chat_service_db` and a runtime role. Run migrations as the migrator role.
2. **Secrets Manager:** `foc/chat-service/runtime` with `DATABASE_URL` and `AMQP_URL`.
3. **ECR and ECS:** repository `chat-service`, Fargate ARM64 task, container port 3005, Service
   Connect name `chat-service`, desired count **1**, private subnets, app security group.
   Set `stopTimeout` to 30 s so connections close cleanly on deploy.
4. **ALB:** listener rule priority 50, path `/chat-service/*`, strip `/chat-service`,
   target group `chat-service-tg` on port 3005, health check `/readyz`. Set the target
   group's deregistration delay to about 30 s. The default 60 s idle timeout is fine because
   the server pings every 25 s.
5. **CloudFront:** behavior `/chat-service/*` to `foc-alb-api`, all HTTP methods, caching
   disabled, origin request policy `AllViewerExceptHostHeader` (this forwards the
   `Sec-WebSocket-*` headers the upgrade needs). CloudFront supports WebSockets with no extra setting.
6. **Order of rollout:** deploy chat-service and routing first, then the frontend that uses it.

### 13.3 Frontend (`user-frontend`)

| File | What |
|---|---|
| `src/api/chat.ts` | REST calls: `getMyChats`, `getChatsForErrand`, `getMessages`, `postMessage`, admin `listErrandChats`, `downloadChatLog` |
| `src/chat/useChatSocket.ts` | React hook: open the socket, send `auth` with a token from `getAccessToken({audience: CHAT_AUDIENCE})`, reconnect with backoff (1 s, 2 s, 5 s, up to 30 s), and get a new token on 4401 |
| `src/chat/ChatPanel.tsx` | Message list and input. On `ready`, fetch `?after=<last id>` to catch up. Show messages immediately as "sending", mark them sent on `ack`, and offer retry on `error`. De-duplicate by message `id`. **States:** `OPEN` shows the normal input. `CLOSING` shows a banner ("This errand has ended. Chat closes in 23 h") and keeps the input. `CLOSED` hides the input and shows "Chat closed". A timer switches `CLOSING` to `CLOSED` at `closesAt`. `room_closing` frames and `room_closed` errors update the state |
| [ErrandDetail.tsx](../user-frontend/src/pages/errands/ErrandDetail.tsx) | Call `GET /errands/{id}/chats`. If the viewer has a room, show `ChatPanel`. A requester with several rooms sees the room for the errand's current courier first and can switch to older ones. If the list is empty right after accepting, retry a few times ("Connecting chat…") |
| Admin review screen ([ReviewQueue.tsx](../user-frontend/src/pages/admin/ReviewQueue.tsx)) | "Download chat log" button, which lists rooms for the errand and downloads each |
| `.env.example` | `VITE_CHAT_AUDIENCE=https://api.foc.local/chat-service` |

Never render message text as HTML. Plain `{message.body}` in JSX is safe.

### 13.4 Later: Redis (when running more than one copy)

1. Write `RedisBroadcaster` ([section 9](#9-broadcast-live-delivery-local-now-redis-later)) and its test.
2. Local: add a `chat-redis` service (copy `order-redis`, no persistence) and set `REDIS_URL`.
3. AWS: ElastiCache for Redis (smallest node) in the private subnets, open port 6379 from the
   app security group, and add `REDIS_URL` to the secret.
4. Raise the ECS desired count to 2. No ALB sticky sessions are needed.
5. Make `/readyz` report Redis status.

Nothing else changes:
- Several copies can share the event queue, because `LockErrand` serialises each errand and
  every handler is idempotent.
- Closing needs no coordination, because it is a time comparison against `closes_at`.
- No business state lives in memory.

---

## 14. Testing

| Level | What | Command |
|---|---|---|
| Unit | config, auth, chat rules (store fake, **fake clock** for the 24-hour window), room status, hub, LocalBroadcaster, protocol parsing | `go test ./...` |
| Integration | store queries, events against real RabbitMQ and Postgres (including the reordering cases in [section 11](#11-events-rabbitmq-consumer)), WebSocket end-to-end with `httptest` | `docker compose -f compose.test.yaml up -d` then `go test -p 1 -tags integration ./...` |
| Migration | up, down, up again; constraint checks (one room per errand and courier, end fields all set or all empty, body length, distinct people) | integration suite |
| Manual end-to-end | Run with `CHAT_CLOSE_DELAY=1m`. Two browsers (requester and courier): accept an errand, chat, refresh, withdraw, accept with another courier (new empty room, old room shows "closing"), complete, wait a minute (both rooms read-only), then export both as admin | local Compose stack |

Pre-commit check (same as credit-service):

```sh
gofmt -l . && go vet ./... && go test ./... && go build ./...
```

---

## 15. Build order

Each phase ends with something that works and can be shown.

| Phase | Build | Check |
|---|---|---|
| 1. Skeleton | Folder layout, `config`, `logging`, `main`/`app`, `/readyz`, Dockerfile, `compose.yaml` (`chat-db`, `chat-service`), `AGENTS.md`, `README.md` | `docker compose up` shows chat-service healthy |
| 2. Database | Full migration (including the end columns and `errand_endings`), `queries/chat.sql`, sqlc (also in the Dockerfile and CI), `store` with integration tests, `chat-migrate` in `compose.yaml` (goose fails on an empty migrations folder, so it waits for this phase), schema-version check in `/readyz` as credit-service does | Tests pass. Tables visible in DBeaver |
| 3. Auth and REST | `auth` (copied), `chat` rules, `httpapi` participant routes incl. `POST` send, `openapi.yaml`. Rooms inserted by hand for now | Send and read messages with `curl` and a real Auth0 token |
| 4. Room creation | `events` consumer for `errand.accepted`, asyncapi update | Accept an errand in the app and a room appears |
| 5. Live delivery | `hub`, `broadcast` (Local), `ws`, protocol doc | Two browser consoles (or `websocat`) exchange messages live |
| 6. Frontend | `api/chat.ts`, `useChatSocket`, `ChatPanel` (open state only), nginx and Vite routes | Requester and courier chat in two browsers |
| 7. Room closing | `errand.withdrawn`, `errand.completed` and `errand.refunded` handlers, `room_closed` check in `Send`, `room_closing` frame, `CLOSING`/`CLOSED` states in `ChatPanel` | With `CHAT_CLOSE_DELAY=1m`: complete an errand, see the banner, and a minute later the chat is read-only |
| 8. Admin export | Admin routes, audit table, text format, review-screen button | Admin downloads the log of a closed room |
| 9. Deploy | RDS, secret, ECR/ECS, ALB rule, CloudFront behavior | Chat works on `app.bestfoc.app` through CloudFront |
| 10. Scale (later) | Redis ([section 13.4](#134-later-redis-when-running-more-than-one-copy)) | Two tasks running. Users on different tasks still chat live |

The closing columns are in the first migration (phase 2) so no second migration is needed, even
though the closing logic is built in phase 7.

---

## 16. Open questions and later work

- **Retention:** chat logs are personal data kept forever. Consider a retention period (for
  example, purge rooms 180 days after `closes_at` unless the errand went to review) and say so
  in the privacy notice.
- **Hiding old rooms:** closed rooms stay in "my chats" as read-only. If the list grows long,
  show only rooms closed in the last 30 days, or move them to an "archived" tab.
- **Account deletion:** user-service blocks business deletion until related checks exist. When it
  arrives, decide whether a deleted user's messages are kept, anonymised or removed, and add an
  internal route for it.
- **Unread notifications:** publish `chat.message_sent` to `foc.events` so the planned
  notification-service can alert someone who is offline. Add it to the asyncapi contract
  first. Never include the message body in the event.
- **Moderation:** let a participant report a message to admins. The export route already gives admins the evidence.
- **Read receipts and typing indicators:** both travel through the broadcaster and need no schema change
  for typing. Read receipts would need a `last_read_message_id` per participant.
