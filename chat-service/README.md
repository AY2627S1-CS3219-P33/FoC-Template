<!--
AI Assistance Disclosure:
Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-10
Scope: Documentation writing and formatting.
Author review: Reviewed, validated, and tested by the authors.
-->

# Chat service

Lets the **requester** and the **assigned courier** of an errand message each other in real time. A room opens when a courier accepts an errand, becomes read-only 24 hours after the errand ends, and is kept in the database afterwards. The design, decisions and build order are in [implementation-plan.md](implementation-plan.md).

**Status:** phases 1 to 7 are done. When a courier accepts an errand, order-service announces it on RabbitMQ and chat-service creates the chat room. The requester and courier chat **live** in a panel on the errand's page in the app, over a WebSocket ([docs/websocket-protocol.md](docs/websocket-protocol.md)), with history and a fallback send over REST ([openapi.yaml](openapi.yaml)). When the errand is completed or refunded, or that courier withdraws, the room stays open for 24 hours and then closes: the app stops showing it, while chat-service keeps it, read-only. The admin export (phase 8) was dropped. Coming next: deployment to AWS (phase 9).

## Contents

- [What each phase built, and why](#what-each-phase-built-and-why)
- [Technology](#technology)
- [Project structure](#project-structure)
- [Running](#running)
- [Trying the API by hand](#trying-the-api-by-hand)
- [Watching events in RabbitMQ](#watching-events-in-rabbitmq)
- [Trying live chat](#trying-live-chat)
- [Trying room closing](#trying-room-closing)
- [Auth0 set-up](#auth0-set-up)
- [Working with the database](#working-with-the-database)
- [Environment variables](#environment-variables)
- [Tests](#tests)

## What each phase built, and why

This section is for readers new to backend development. It explains what each part is for and what would go wrong without it.

### Phase 1: skeleton

| Part | What it does | Why it matters |
|---|---|---|
| Configuration ([internal/config](internal/config/config.go)) | Reads every setting from environment variables at startup, checks them all, and reports every problem in one error. | A wrong setting stops the service at startup with a clear message, not hours later on the first request that needs it. Errors never show values, because URLs contain passwords. |
| Readiness check (`GET /readyz`) | Answers `200` only when the service can do its job, `503` otherwise. | Docker Compose and the AWS load balancer use it to decide whether to send traffic to this copy of the service. |
| Dockerfile and `compose.yaml` | Build the service into a small container and start it with its database. | Every teammate, CI and AWS run the same thing, so "works on my machine" problems go away. |

### Phase 2: database

#### 1. A full migration ([migrations/00001_create_chat_schema.sql](migrations/00001_create_chat_schema.sql))

**What a migration is.** A migration is a numbered SQL file that changes the database's structure: it creates tables, adds columns, and so on. A tool called **goose** applies migrations in number order and records which ones it has applied in a table called `goose_db_version`. Each file has two halves: `-- +goose Up` (make the change) and `-- +goose Down` (undo it).

**Why use migrations instead of creating tables by hand?** Every database has to end up with exactly the same structure: your laptop, your teammates' laptops, CI and the AWS database. If people typed `CREATE TABLE` by hand, the databases would slowly drift apart and bugs would appear on one machine only. With migrations, the files in Git *are* the structure, and goose makes any database match them.

**Why "full"?** This first migration creates *every* table and column the finished service needs, even the ones only used in later phases:

- `chat_rooms`: one row per (errand, courier) pair. It includes the closing columns (`ended_at`, `end_reason`, `closes_at`), although the closing logic is only built in phase 7.
- `chat_messages`: every message, never deleted.
- `errand_endings`: remembers that an errand completed or was refunded, for the case where that event arrives *before* the room is created (RabbitMQ may deliver events out of order).

Designing the whole schema now has three benefits:

1. **No second migration later.** Changing a table that already holds real data is the riskiest kind of database change: you have to think about existing rows, locks on large tables, and deploying old and new code side by side. Getting the structure right once avoids that work.
2. **The design is checked early.** Writing every table forced us to think through the tricky cases (duplicate events, events in the wrong order, a courier withdrawing) before any code depends on the structure.
3. **Later phases only add code.** Phases 3 to 7 write Go, not SQL schema changes.

**The database enforces the rules too.** The tables have *constraints*: rules PostgreSQL itself refuses to break, whatever the code does. For example:

| Constraint | Rule | What it protects against |
|---|---|---|
| `chat_rooms_one_per_assignment` | One room per (errand, courier) | A repeated `errand.accepted` event creating a second room |
| `chat_rooms_distinct_people` | Requester ≠ courier | Someone chatting with themselves |
| `chat_rooms_end_fields` | `ended_at`, `end_reason` and `closes_at` are all set or all empty, and a room cannot close before it ends | Half-ended rooms that would never close |
| `chat_messages_idempotent` | One message per (room, `client_msg_id`) | A browser retrying a send and posting the message twice |
| `chat_messages_body_length` | 1 to 2000 characters | Empty or huge messages |
| Foreign keys (`REFERENCES chat_rooms`) | Messages belong to an existing room, and a room with messages cannot be deleted | Orphan messages, and lost chat history |

Even if a future bug skipped a check in Go, the database would still refuse bad data. The tests found a real gap this way: the planned `chat_rooms_end_fields` rule allowed a room with an end time but no closing time, because in SQL a comparison with an empty (`NULL`) value is "unknown", and a constraint only rejects "false". The rule now also requires `closes_at IS NOT NULL`.

**Why can it go both up and down?** The `Down` half undoes the `Up` half. A test applies the migration, undoes it, and applies it again, so if a deployment goes wrong the change can be rolled back cleanly.

**Who applies it?** A separate one-shot container, `chat-migrate`, runs goose before the service starts. The service itself never changes the database structure. On AWS the service's database user will only be allowed to read and write rows, so a bug or an attacker cannot drop tables through it.

#### 2. Queries and sqlc ([queries/chat.sql](queries/chat.sql), [sqlc.yaml](sqlc.yaml))

Every SQL statement the service runs lives in one file, `queries/chat.sql`. A tool called **sqlc** reads that file together with the migrations and *generates* Go functions for it (in `internal/database/chatgen`, not committed to Git).

**Why?** If you write SQL as strings inside Go code, a typo in a column name, or a column renamed in a migration, is only discovered when that line runs, possibly in production. sqlc checks every query against the real schema when the code is generated, and gives you typed Go functions, so mistakes are found at build time. The Docker build and CI regenerate the code, so they always match the schema.

Some queries are written so that **repeating them is harmless** (this property is called *idempotence*). RabbitMQ may deliver the same event twice, and browsers retry sends when the network drops:

- `UpsertRoomOnAccept` creates a room, or does nothing if it already exists.
- `InsertMessage` ignores a second message with the same `client_msg_id`.
- `EndRoom` only ever moves a room's end time *earlier*, so applying the same event twice, or an older event late, changes nothing.

`LockErrand` takes a PostgreSQL *advisory lock* per errand, so two events for the same errand are applied one after the other, even when several copies of the service run.

#### 3. The store ([internal/store](internal/store/))

The store is the **only** package that talks to the database. The rest of the service calls methods like `GetRoom` or `InsertMessage` and gets back plain Go types from [internal/chat](internal/chat/types.go) (`Room`, `Message`), never database types.

**Why a separate layer?**

- **One place for SQL.** When a query needs to change, there is exactly one place to look.
- **Business rules stay testable.** The rules in phase 3 (who may read, who may send) can be tested with a fake store in memory, with no database.
- **Errors are translated.** A database error like "violates check constraint chat_messages_body_length" becomes `chat.ErrInvalidMessage`, which the API can turn into a clear `400` response. Failures that a retry might fix, such as a lost connection, become `chat.ErrUnavailable`, so the event consumer knows to try again later. Raw database errors never leave the store, because they can contain message text or connection details.
- **Transactions.** `WithTx` runs several queries as one unit: either all of them are saved, or none are. This matters when, for example, a room is created and immediately ended in one event.

The store also provides the schema check used by `/readyz`: the service reports *not ready* until the database has every migration this build expects. If someone forgets to run a migration, the problem shows up immediately as an unhealthy service instead of as errors on the first request.

`chat.Room.Status(now)` works out whether a room is `OPEN`, `CLOSING` or `CLOSED` by comparing `closes_at` with the current time. The status is never stored, so no background job is needed to close rooms, and every copy of the service always agrees.

#### 4. Integration tests

*Unit* tests check code on its own. *Integration* tests check it against a real PostgreSQL, because many of the important rules live in the database: constraints, `ON CONFLICT`, locks. These tests:

- apply the migration, undo it, and apply it again;
- try to break each constraint and check PostgreSQL refuses;
- run every query in the store, including the tricky cases: duplicate events, `errand.withdrawn` before `errand.accepted`, a withdrawn courier's room keeping its earlier end time when the errand later completes, and a second transaction waiting for another's errand lock.

Each test gets its **own fresh schema** (a separate namespace inside the test database), so tests never see each other's data, and the schema is deleted afterwards.

### Phase 3: authentication and the REST API

Phase 3 makes the service usable: a logged-in user can list their chats, read messages and send messages. A request passes through four layers, each with one job:

```
 HTTP request ──► auth middleware ──► httpapi handler ──► chat.Service ──► store ──► PostgreSQL
                  "who are you?"      "read the request,   "is this          "run the
                  "may you use        write the response"   allowed?"         SQL"
                   chats at all?"
```

#### 1. Authentication ([internal/auth](internal/auth/))

**What it does.** Every request must carry an **access token** from Auth0, the login service the whole app uses: `Authorization: Bearer <token>`. The token is a **JWT** (JSON Web Token), a small JSON document that Auth0 has digitally signed. Among other things it says:

- `sub`: who the user is, e.g. `auth0|6543...`;
- `aud` (*audience*): which API it was issued for, here `https://api.foc.local/chat-service`;
- `exp`: when it expires;
- `permissions`: what the user may do, e.g. `["chats:use"]`.

The middleware checks the signature with Auth0's public keys (downloaded once and cached), then checks the issuer, the audience and the expiry. Only if everything matches does the request continue, carrying a trusted `Principal` (user ID and permissions).

**Why it matters.**

- **Nobody can pretend to be someone else.** Changing one letter of a token breaks the signature. The user ID always comes from the verified token, never from the request body or URL. The API has no `senderId` field at all, and an unknown field in the body is rejected.
- **A token for another service is refused.** A token issued for order-service has a different audience, so chat-service rejects it. A leaked token therefore only works for the one API it was issued for.
- **Permissions, not just identity.** Every chat route requires the `chats:use` permission, the only permission chat-service has. Machine-to-machine tokens (used by services, not people) are refused on these routes.

The package is **copied from order-service** (the plan asks for this) and only the permission names changed. Security code is easy to get subtly wrong, so it is reused rather than rewritten, together with its tests: expired tokens, wrong audiences, forged signatures, rotated keys and an Auth0 outage are all covered.

| Response | When |
|---|---|
| `401 UNAUTHENTICATED` | No token, or the token is invalid, expired, or for another API |
| `403 FORBIDDEN` | A valid token without `chats:use`, or a machine-to-machine token |
| `503 DEPENDENCY_UNAVAILABLE` | Auth0's keys cannot be downloaded right now |

#### 2. The chat rules ([internal/chat/service.go](internal/chat/service.go))

**What it does.** `chat.Service` is the **single place that decides what is allowed**:

| Rule | Why |
|---|---|
| Only the room's requester and courier can read or send. Anyone else gets `404 ROOM_NOT_FOUND`, the same answer as for a room that does not exist. | Privacy. A `403 Forbidden` would reveal that the room exists. With `404`, an outsider learns nothing, not even whether a room ID is real. |
| Courier B never sees courier A's room for the same errand. | Each (errand, courier) pair has its own room (decided in the plan), and the participant check enforces it. |
| Reading is allowed in every state, even `CLOSED`. | People can look back at what was agreed. |
| Sending is refused with `409 ROOM_CLOSED` once the room is `CLOSED`, and allowed while `OPEN` or `CLOSING`. | A finished errand's chat becomes read-only after 24 hours. The time is compared using the **server's** clock, so a user cannot change their computer's clock to keep sending. |
| The message body is trimmed and must be 1 to 2000 characters (characters, not bytes, so `é` and emoji count as one), with no invisible control characters except newline and tab. | Stops empty messages, huge messages, and invisible characters that can hide text or break displays. |
| The text is stored exactly as typed, including `<b>` or `<script>`. | Escaping is the display's job (React does it automatically). Changing text on the way in tends to corrupt it, and does not make it safe anyway. |
| A send with a `clientMsgId` already used in the room returns the stored message (`200`) instead of creating a new one (`201`). | **Safe retries.** If the network drops after the server saved a message but before the browser got the answer, the browser sends again with the same ID, and the message still appears only once. |

**Why one central place?** Messages will later arrive by REST (now) and by WebSocket (phase 5), and rooms change through RabbitMQ events (phase 4). If each of those checked the rules itself, they would slowly drift apart, and one path would end up with a security hole. With one `Service`, every path gets the same rules.

**Why it is easy to test.** The service doesn't talk to PostgreSQL directly. It uses a `Store` *interface* (a list of methods it needs). In tests, an in-memory fake ([chattest](internal/chat/chattest/store.go)) and a fake clock stand in, so the test can jump 24 hours ahead in a millisecond to check "sending works while closing, and is refused one second after `closesAt`".

#### 3. The REST API ([internal/httpapi](internal/httpapi/), [openapi.yaml](openapi.yaml))

**What it does.** The handlers translate between HTTP and the chat rules. They read the URL, query and JSON body, call `chat.Service`, and turn the answer into JSON with the right status code.

| Route | What it returns |
|---|---|
| `GET /me/chats` | All my rooms, newest first, each with `status`, `closesAt` and the last message |
| `GET /errands/{errandId}/chats` | My rooms for one errand (empty list if none yet) |
| `GET /chats/{roomId}/messages?after=&before=&limit=` | A page of messages, oldest first |
| `POST /chats/{roomId}/messages` with `{"clientMsgId": "...", "body": "..."}` | `201` with the new message, or `200` for a retry |

In the browser these sit under `/chat-service/...` (nginx and the Vite dev server strip the prefix).

**Why the pieces matter.**

- **Paging (`after`, `before`, `limit`).** A chat can grow long, so it is read a page at a time (at most 100 messages). Message IDs only ever increase, so "give me everything after message 57" is the exact catch-up query a browser needs after a reconnect, and "before 57" is scrolling back.
- **One error format.** Every error looks like `{"code": "ROOM_CLOSED", "message": "..."}`, the same shape as order-, credit- and supplier-service. The frontend branches on `code`, which never changes. `message` is for people and may change.
- **No internal details in errors.** A database failure is logged on the server and answered with a generic `503` or `500`. Connection strings, SQL errors and message text never appear in a response.
- **`openapi.yaml` is the contract.** It describes every route, parameter, response and error code in a standard format. The frontend team can build against it, tools can render it as documentation, and CI checks that it is valid. The plan asks for it to be written alongside the routes, as the other services do.

The room status (`OPEN`, `CLOSING`, `CLOSED`) is calculated with the server's clock each time a room is returned, and `closesAt` is included, so the browser can show "chat closes in 5 h" and switch to read-only by itself at that moment.

#### 4. Tests

| Level | What is covered |
|---|---|
| auth (copied) | valid token, wrong audience, wrong issuer, expired, forged or unknown key, missing permission (`403`), machine token on a user route (`403`), Auth0 down (`503`) |
| chat rules (no database, fake clock) | everything in the rules table above, including the four cases the plan lists |
| HTTP handlers (fake login and store) | every route's success, `400`, `401`, `403`, `404`, `409` and `503` cases, and that no internal detail leaks |
| end to end ([chat_e2e_test.go](internal/app/chat_e2e_test.go)) | the real app, a real PostgreSQL, a room inserted by hand and real RS256-signed tokens from a local key server: send, retry, read, outsider `404`, wrong audience `401` |

### Phase 4: creating rooms from errand events

Until now, rooms had to be inserted by hand. Phase 4 creates them automatically: when a courier accepts an errand in the app, a chat room for that requester and courier appears, usually within a second.

#### 1. How chat-service hears about errands: events and RabbitMQ

chat-service never calls order-service and never reads its database (each service owns its own data). Instead, order-service **announces** what happens to an errand by publishing an **event**, a small JSON message such as *"errand 7b0c… was accepted by courier X"*. Events travel through **RabbitMQ** (the `queue-service` container), a *message broker*: a post office between services.

```
 order-service ──publishes──► foc.events ──errand.accepted──► chat-service.errand-events ──► chat-service
 (courier accepts)            (exchange)     (routing key)       (chat-service's queue)        (creates the room)
```

- An **exchange** (`foc.events`) receives every event and sorts it by its **routing key** (`errand.accepted`, `errand.completed`, …).
- A **queue** is a mailbox. chat-service has its own, `chat-service.errand-events`, and **binds** it to the routing keys it cares about. Phase 4 bound only `errand.accepted`; phase 7 added `errand.withdrawn`, `errand.completed` and `errand.refunded`.
- The **consumer** ([internal/events/consumer.go](internal/events/consumer.go)) is a background loop inside chat-service that takes events from the queue one by one.

**Why events instead of order-service calling chat-service?** The two services stay independent. order-service doesn't know or care that chat exists; it only announces facts. If chat-service is down or restarting, nothing fails in order-service: the events simply **wait in the queue** (it is *durable*: it survives restarts) and are processed when chat-service comes back. Other services, such as notifications, can listen to the same events without changing order-service.

The message format is a shared contract, [docs/asyncapi.yaml](../docs/asyncapi.yaml). chat-service is now listed there as a consumer of `errand.accepted`.

#### 2. Handling an event safely ([internal/events](internal/events/), [internal/chat/lifecycle.go](internal/chat/lifecycle.go))

Each event goes through three steps:

1. **Check it** ([message.go](internal/events/message.go)). The JSON must match the contract: an errand ID that is a UUID, a version, the requester and the courier, and so on. Fields chat-service doesn't use are ignored, so order-service can add new ones without breaking chat.
2. **Apply it** (`chat.Lifecycle.OnAccepted`), in **one database transaction**: lock the errand, create the room, and, if the errand already finished (see below), mark the room as ending.
3. **Answer RabbitMQ** ([handler.go](internal/events/handler.go)). Only *after* the transaction is saved does the consumer **acknowledge** ("ack") the message, telling RabbitMQ to delete it. If chat-service crashed halfway, the message was never acked, so RabbitMQ delivers it again and nothing is lost.

RabbitMQ promises **at-least-once** delivery: an event can arrive **twice**, and events can arrive **out of order**. The handling is designed for both:

| Situation | What happens | Why it is safe |
|---|---|---|
| The same `errand.accepted` arrives twice | The second one finds the room already there and changes nothing | The "one room per errand and courier" rule from phase 2 makes creating a room *idempotent* (doing it twice equals doing it once) |
| `errand.completed` was processed before `errand.accepted` | The room is created already ending at the completion time | The completion was remembered in `errand_endings` |
| `errand.withdrawn` was processed before `errand.accepted` | The accept fills in the details but does not reopen the room | The room already exists and ended; the accept only adds missing data |
| Two copies of chat-service handle events for the same errand at once | The second waits for the first | `LockErrand` takes a database lock per errand |

(The completed-first and withdrawn-first cases can only happen since phase 7 binds those events. `OnAccepted` already handled them, so phase 7 didn't have to change it.)

#### 3. When something goes wrong

Not every event can be applied straight away. The consumer sorts failures into three kinds and treats each differently:

| Problem | Example | What the consumer does |
|---|---|---|
| The message is broken | Not JSON, missing courier, requester and courier are the same person | Sends it to the **dead-letter queue** (`chat-service.errand-events.dlq`) at once. Retrying cannot fix it |
| A temporary failure | The database is down or restarting | **Keeps retrying**, waiting a little longer each time (1 s, 2 s, 4 s, … up to 30 s), until it works |
| An unexpected error | A bug in chat-service | Retries 5 times, then dead-letters it, so one bad message cannot block every message behind it |

A **dead-letter queue** is where messages go that could not be processed. Nothing reads it automatically. It keeps them so a developer can look at what went wrong (in the RabbitMQ management UI, see [below](#watching-events-in-rabbitmq)) instead of losing them.

**Why keep retrying when the database is down, instead of giving up?** order-service sends each event once. If chat-service gave up and dead-lettered an `errand.accepted` during a short database outage, that room would never exist. Waiting is always right for a temporary failure.

**Something the tests revealed.** The plan relied on RabbitMQ's `x-delivery-limit` setting to stop a message that fails forever. The tests showed that RabbitMQ 4 doesn't count a consumer *saying* "try again later" towards that limit; it only counts deliveries where the consumer disappeared without answering, for example by crashing. So chat-service counts its own retries for unexpected errors, and keeps the RabbitMQ limit as protection against crashes. Both behaviours have tests.

#### 4. Health: `ready` or `degraded`

`/readyz` now also reports the consumer:

```json
{"status":"ready","dependencies":{"postgres":"ok","errandEvents":"ok"}}
{"status":"degraded","dependencies":{"postgres":"ok","errandEvents":"down"}}
```

If RabbitMQ is unreachable, chat-service still answers **200**, but `degraded`. Existing chats keep working, and new rooms appear as soon as the consumer reconnects, because the events wait in the queue. Only a missing database makes it `503 not_ready`. (The plan had suggested failing readiness without RabbitMQ. On AWS that would make the load balancer replace a working chat-service again and again during a RabbitMQ outage, taking every chat offline for no gain.)

#### 5. Tests

| Level | What is covered |
|---|---|
| message checks (no services) | a real order-service message is accepted; 15 kinds of broken message are refused |
| handler decisions (no services) | which failures are acked, retried, retried a few times, or dead-lettered |
| lifecycle (real PostgreSQL) | a room is created once; duplicates; completion before acceptance; withdrawal before acceptance; a new courier gets a new room |
| consumer (real RabbitMQ and PostgreSQL) | an event creates the room and a duplicate does not; broken messages reach the dead-letter queue; a database outage is retried until it ends; an event failing unexpectedly is dead-lettered after 5 tries; RabbitMQ's own limit catches crashes; the queue set-up matches order-service's |
| end to end ([events_e2e_test.go](internal/app/events_e2e_test.go)) | the whole app with real PostgreSQL and RabbitMQ: `/readyz` reports the consumer, an event is published, the room appears |

### Phase 5: live delivery over WebSockets

Until now a message only appeared for the other person when their browser asked for it again. Phase 5 makes chat **live**: the moment one person sends, the other sees it, and a new room appears on both screens the moment a courier accepts.

#### 1. Why a WebSocket

Normal HTTP is *request → response*: the browser asks, the server answers, done. The server can never speak first, so the only way to see new messages would be to ask every few seconds (*polling*). That is slow (up to a few seconds late) and wasteful (thousands of "anything new?" requests answered "no").

A **WebSocket** starts as an HTTP request (`GET /ws`), then both sides agree to *upgrade* it into a connection that stays open. Over it, either side can send a message, called a **frame**, at any time. The server pushes a new message the instant it is saved.

The frames are documented for frontend developers in [docs/websocket-protocol.md](docs/websocket-protocol.md), and defined once in Go in [internal/protocol](internal/protocol/protocol.go). REST and WebSocket share the same room and message JSON and the same error codes, so a browser handles both the same way.

#### 2. How a message travels

```
 Courier's tab                                                         Requester's tab(s)
      │ {"type":"send",...}                                                   ▲
      ▼                                                                       │ {"type":"message",...}
   ws (read loop) ──► chat.Service.Send ──► PostgreSQL (saved, committed)     │
      │ {"type":"ack"}            │                                           │
      ◄───────────────            └──► Notifier ──► Broadcaster ──► Hub ──────┘
                                       (builds the     (Local now,     (this copy's open
                                        frame)          Redis later)    connections, by user)
```

| Part | Job | Why it is separate |
|---|---|---|
| [ws](internal/ws/ws.go) | Runs one connection from start to finish: checks the page's origin, reads the `auth` frame, then reads sends, writes frames and pings until the connection ends | All the WebSocket details live in one place |
| [chat.Service](internal/chat/service.go) | Same rules as REST, then, **only after the message is saved**, tells the Notifier | "Save, then push": nobody is ever shown a message that could still be lost. A retried send pushes nothing |
| [Notifier](internal/broadcast/notifier.go) | Turns "message saved" or "room opened" into frames for the room's two people | Each person gets a `room_opened` with their own `myRole` |
| [Broadcaster](internal/broadcast/broadcast.go) | "Deliver this frame to these users, wherever they are connected" | The **only** part that changes when chat-service runs as several copies (see below) |
| [Hub](internal/hub/hub.go) | Remembers which connections are open in this copy, keyed by user (a user can have several tabs) | The only thing chat-service keeps in memory |

The REST `POST /chats/{roomId}/messages` goes through the same `chat.Service.Send`, so a message sent over REST is pushed live as well.

#### 3. Keeping connections safe and healthy

| Rule | Why |
|---|---|
| **Log in with the first frame**, `{"type":"auth","token":"..."}`, within 5 s | Browsers cannot add an `Authorization` header to a WebSocket, and a token in the URL would be written to server logs. A connection that doesn't log in quickly is closed (`4401`), so idle sockets don't pile up |
| **Only allowed pages may connect** (`WS_ALLOWED_ORIGINS`) | Otherwise any website you visit could open a chat connection in your name. This is the WebSocket version of CSRF protection |
| **The connection closes when the token expires** (`4401`) | A token is valid for a limited time; an open socket must not outlive it. The browser fetches a fresh token and reconnects |
| **At most 5 connections per user**, **5 messages per second** per connection | One account (or a buggy script) cannot use up the server or flood a chat |
| **A ping every 25 s** | AWS's load balancer drops connections that are silent for 60 s; pings keep them open, and a ping that gets no answer reveals a dead connection |
| **Slow tabs are dropped**, not waited for | Each tab has a small queue of 32 frames. If a tab can't keep up, it is disconnected (`1008`) instead of slowing down everyone else, and it catches up over REST when it reconnects |
| **On shutdown, every socket gets `1012` "service restart"** | During a deploy, browsers reconnect at once (to the new copy) instead of waiting for a timeout |

Close codes tell the browser what to do next: reconnect now, get a new token first, or give up. The full table is in [docs/websocket-protocol.md](docs/websocket-protocol.md#4-closing-and-reconnecting).

#### 4. Ready for several copies later (Redis)

Today one copy of chat-service runs, so the `Local` broadcaster hands frames straight to its own hub. With two copies behind the load balancer, the requester might be connected to copy 1 and the courier to copy 2. A `RedisBroadcaster` (phase 10) will publish each frame to Redis, and every copy will deliver it to the connections it holds. Nothing else changes, because:

- rooms, membership and messages always come from PostgreSQL, never from memory;
- frames are addressed to **users**, not connections, so any copy can deliver them;
- a frame lost in transit costs nothing: the message is already saved, and the browser catches up over REST when it reconnects.

#### 5. Tests

| Level | What is covered |
|---|---|
| hub | delivers to exactly the listed users and all their tabs; per-user connection limit; a slow client is dropped without blocking others; concurrent use |
| broadcast | `Local` delivers to exactly the listed recipients; message, personalised `room_opened` and `room_closing` frames; a failed publish never fails the sender |
| chat | a send is pushed once, only after saving; retries and refused sends push nothing; only new rooms are announced |
| WebSocket ([ws_test.go](internal/ws/ws_test.go), real WebSocket clients) | the plan's four cases (A sends: B gets `message`, A gets `ack`, a third user gets nothing; a resend stores one row; no `auth` in time closes with `4401`; a closed room returns `ROOM_CLOSED`), plus bad and expired tokens, missing permission, foreign origins, connection limit, rate limit, malformed frames, heartbeat, `room_opened` and shutdown with `1012` |
| end to end ([live_e2e_test.go](internal/app/live_e2e_test.go)) | the whole app with real PostgreSQL, RabbitMQ and signed tokens: both people are connected when the courier accepts, both get `room_opened`, they chat live over WebSocket and REST, and both get `1012` on shutdown |

### Phase 6: the chat panel in the app

Phase 6 puts chat where people need it: on the errand's page in `user-frontend`, for its requester and its courier. Nothing in chat-service changed; this phase is the browser side of the contracts from phases 3 and 5.

#### 1. Getting there: one route for REST and the WebSocket

The browser only ever talks to its own origin. Requests to `/chat-service/...` are forwarded to chat-service with the prefix removed, so `GET /chat-service/me/chats` reaches `GET /me/chats`:

| Where | Who forwards | Extra for the WebSocket |
|---|---|---|
| Docker Compose (`http://localhost:8081`) | nginx in `user-frontend` ([nginx.conf](../user-frontend/nginx.conf)) | passes the `Upgrade` and `Connection` headers on (nginx drops them otherwise), and allows an hour of silence (the server pings every 25 s anyway) |
| `npm run dev` (`http://localhost:5173`) | the Vite proxy ([vite.config.ts](../user-frontend/vite.config.ts)) | `ws: true` |

The page's own origin (`localhost:8081` or `localhost:5173`) reaches chat-service unchanged, and both are in the default `WS_ALLOWED_ORIGINS`.

#### 2. The pieces

| File | Job |
|---|---|
| [api/chat.ts](../user-frontend/src/api/chat.ts) | REST calls (`getMyChats`, `getChatsForErrand`, `getMessages`, `postMessage`), the room/message/frame types, and the error codes turned into sentences |
| [chat/useChatSocket.ts](../user-frontend/src/chat/useChatSocket.ts) | A React hook that opens the socket, sends the `auth` frame with a fresh token on every connect, and reconnects after 1 s, 2 s, 5 s … up to 30 s (with some randomness). It follows the close codes: `1012` comes back at once, `4429` waits the longest, `4403` stops for good |
| [chat/ErrandChat.tsx](../user-frontend/src/chat/ErrandChat.tsx) | The errand's chat section. Loads the errand's rooms, owns the one socket, and shows one room at a time; a requester whose first courier withdrew can switch between couriers' rooms |
| [chat/ChatPanel.tsx](../user-frontend/src/chat/ChatPanel.tsx) | One conversation: history, live messages, the message box |

#### 3. Rules the panel follows, and why

| Rule | Why |
|---|---|
| **After every `ready`, fetch `?after=<last id>`** | The socket never replays what was missed while disconnected (laptop asleep, a deploy). The database has it, so the panel asks for it |
| **Merge by message `id`** | The same message can arrive three ways: the `ack` to your send, the `message` frame, and a catch-up. Keyed by `id`, it is shown once |
| **Show your message at once as "Sending…"**, then swap in the stored one | Typing feels instant. The stored copy is matched by `clientMsgId` and replaces the placeholder |
| **No answer in 10 s, or an `error` frame → "Retry"**, reusing the same `clientMsgId` | The server stores a message once per `clientMsgId`, so a retry after a lost `ack` never posts it twice |
| **No live connection → send over REST** | `POST /chats/{id}/messages` has the same rules and still pushes the message live to the other person. While reconnecting, the panel also checks for new messages every 15 s |
| **"Connecting chat…" right after accepting** | The room appears when chat-service processes the accept event, usually within a second. The section picks it up from the `room_opened` frame, a re-read after `ready`, or a short poll, and offers "Try again" after 15 s |
| **Message text is rendered as text**, never as HTML | It is typed by another user. React escapes `{message.body}`; `white-space: pre-wrap` keeps their line breaks |

In phase 6 the panel handled OPEN rooms only; phase 7 added the closing banner and the read-only state.

### Phase 7: closing rooms when the errand ends

A chat is for one errand, so it should not stay open forever. Phase 7 closes it, gently: when the errand ends, the room stays writable for **24 hours** (both people may still need to say "left it with the guard"), then **closes**. A closed room is read-only in chat-service and is never deleted, but the app no longer shows it to the two people.

#### 1. What ends a room

| Event from order-service | Rooms it ends |
|---|---|
| `errand.completed` (delivery confirmed, or an admin completed it) | every room of the errand |
| `errand.refunded` (an admin refunded it after review) | every room of the errand |
| `errand.withdrawn` (the courier withdrew, or missed the pickup deadline) | only **that courier's** room; a later courier gets a new one |

Delivery and disputes do **not** end the room: the requester may need the courier while checking a delivery or during a review.

#### 2. Three states, no timer job

The room's state is **worked out**, never stored, from one column, `closes_at`:

| `closes_at` | State | Sending |
|---|---|---|
| empty | `OPEN` | yes |
| in the future | `CLOSING` | yes |
| in the past | `CLOSED` | no: `409 ROOM_CLOSED` (REST) or an `error` frame (WebSocket) |

The event only **sets** `closes_at` = the event's `occurredAt` + `CHAT_CLOSE_DELAY` (24 h). Nothing has to run at the moment a room closes: every read and send compares `closes_at` with the clock. That is why no scheduler or background job is needed, why a closing can never be "missed", and why it works the same on every copy of the service.

**Why `occurredAt` and not "now"?** If chat-service was down for an hour, the completion is processed an hour late. Counting from when the errand *actually* ended keeps the 24 hours honest.

#### 3. Events that arrive twice or out of order

The handlers ([lifecycle.go](internal/chat/lifecycle.go): `OnWithdrawn`, `OnErrandEnded`) follow one rule: **the earliest ending wins.** Setting an ending only ever moves it earlier, never later:

| Situation | What happens |
|---|---|
| Any of these events arrives twice | The second changes nothing and tells nobody |
| Courier A withdrew, B accepted, then the errand completed | B's room ends at the completion; A's keeps its earlier withdrawal time |
| `errand.withdrawn` arrives before `errand.accepted` | The room is created already ending; the late acceptance cannot reopen it |
| `errand.completed` arrives before `errand.accepted` | No room yet, so the ending is remembered (`errand_endings`); the late acceptance creates the room already ending |
| A withdrawal from before the completion arrives after it | The ending moves earlier, to the withdrawal |

As in phase 4, each handler runs in one transaction behind the errand's lock, and RabbitMQ is acked only after it commits.

#### 4. Telling the browsers

Only after the commit, each room whose ending changed gets a `room_closing` frame (`roomId`, `endReason`, `closesAt`) to its two people. A repeat of the event sends nothing.

There is **no "room closed" frame**. The browser already knows `closesAt` and hides the room on its own timer. If the browser's clock is behind, the server's `ROOM_CLOSED` answer to the next send hides it at once.

In the app ([user-frontend/src/chat](../user-frontend/src/chat/roomStatus.ts)):

- **CLOSING:** a banner such as *"This errand is complete. You can keep chatting until 10 Nov, 14:40 (in 23 h 59 min); after that this chat closes and is no longer shown here."* The message box stays.
- **CLOSED:** the room disappears from the page, without a refresh. If it was the only one, the chat section disappears too. **This is only hidden in the app:** the REST API still lists closed rooms and serves their history (read-only), as the contract says.
- A requester with several couriers' rooms sees the ones still open or closing in the conversation list, marked `· closing` where it applies.
- A courier who withdrew sees the errand's public page, but still finds their own room there while it closes.
- When no room on the page is shown and no courier is expected, the page opens no WebSocket at all.

#### 5. Tests

| Level | What is covered |
|---|---|
| message checks (no services) | real `errand.withdrawn`, `errand.completed` and `errand.refunded` messages decode; a withdrawal without `courierId` is refused; each routing key reaches its handler |
| lifecycle (real PostgreSQL) | completion and refund end the room; a withdrawal ends only that courier's room; duplicates change nothing and announce nothing; withdrawal before acceptance; completion before acceptance; A's earlier withdrawal survives the later completion; a late but earlier withdrawal moves the ending earlier; with a fake clock, a real event leaves the room writable while `CLOSING`, refuses a send one second after `closesAt`, and still allows reading |
| consumer (real RabbitMQ and PostgreSQL) | the plan's flows through the broker: accepted then completed (each twice), refunded, withdrawn before accepted (each twice), completed before accepted, withdrawal → new courier → completion; a withdrawal without a courier is dead-lettered |
| end to end ([closing_e2e_test.go](internal/app/closing_e2e_test.go)) | the whole app with a 2-second delay: both connected people get `room_closing` with the right `closesAt`, the room lists as `CLOSING` and still accepts a message, then lists as `CLOSED`, refuses a send with `ROOM_CLOSED`, and still serves its history |

**Rooms from before phase 7.** Events are only routed to the queue once their routing key is bound, so a completion that happened before this build ran was never delivered to chat-service, and its room stays `OPEN`. That only affects local stacks used before now; nothing is deployed yet.

## Technology

Go, `net/http`, `pgx/v5` (PostgreSQL driver), `goose` (migrations), `sqlc` (typed SQL), Auth0 JWTs (`go-jwt-middleware`, `jwx`), RabbitMQ (`amqp091-go`), WebSockets (`github.com/coder/websocket`), `log/slog` JSON logs. Planned: Redis (phase 10).

## Project structure

```
cmd/chat-service/         entry point
internal/app/             composition root and HTTP server lifecycle
internal/config/          environment configuration
internal/auth/            Auth0 token verification and permission middleware (copied from order-service)
internal/auth/authtest/   local key server and token signing for tests
internal/apperror/        error codes and the JSON error body
internal/chat/            business types (Room, Message, Status) and rules (Service)
internal/chat/chattest/   in-memory Store and fake clock for tests
internal/store/           the only package that runs SQL: queries, transactions, error mapping
internal/database/chatgen/  sqlc output (generated, git-ignored)
internal/httpapi/         REST routes: request parsing, JSON responses, error mapping
internal/events/          RabbitMQ: queue set-up, message checks, consumer for errand events
internal/ws/              GET /ws: one WebSocket connection, from handshake to close
internal/hub/             open connections in this copy, by user
internal/broadcast/       Broadcaster (Local now, Redis later) and the Notifier that builds frames
internal/protocol/        wire format: room and message JSON, WebSocket frames, close codes
internal/readiness/       GET /readyz
internal/testdb/          per-test migrated PostgreSQL schema
internal/logging/         slog setup
migrations/               goose SQL migrations (+ embedded for tests and /readyz)
queries/chat.sql          every SQL statement, input for sqlc
sqlc.yaml                 sqlc settings
openapi.yaml              REST contract
docs/websocket-protocol.md  WebSocket contract
```

## Running

With the whole stack, from the repository root:

```sh
docker compose up --build chat-service
curl localhost:3006/readyz        # {"status":"ready","dependencies":{"errandEvents":"ok","postgres":"ok"}}
docker compose ps chat-service    # STATUS shows (healthy)
```

This starts the containers in order: `chat-db` (PostgreSQL), `chat-migrate` (applies migrations, then exits), `queue-service` (RabbitMQ), and `chat-service`. `chat-service/.env` is optional for now: Compose supplies local defaults, including a placeholder Auth0 issuer. Copy `.env.example` to `.env` to set the real tenant or other options.

Without `chat-service/.env`, the placeholder issuer means every token is refused with `401`; see [Trying the API by hand](#trying-the-api-by-hand).

On the host (needs a reachable, migrated PostgreSQL):

```sh
cp .env.example .env              # set DATABASE_URL and AUTH0_ISSUER
go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate
go run ./cmd/chat-service
```

`/readyz` returns `503 {"status":"not_ready"}` when PostgreSQL does not answer or its schema is older than this build, and `200` with `"degraded"` while the RabbitMQ consumer is not subscribed.

## Trying the API by hand

Rooms are now created when a courier accepts an errand in the app (see [Watching events in RabbitMQ](#watching-events-in-rabbitmq)). To test without the frontend, you can still create a room yourself. You need two test accounts in your Auth0 tenant (one acts as requester, one as courier), and the [Auth0 set-up](#auth0-set-up) below done once.

**1. Set the real Auth0 tenant**, so the service accepts real tokens, then restart:

```sh
cp chat-service/.env.example chat-service/.env    # set AUTH0_ISSUER=https://<your-tenant>/
docker compose up -d --build chat-service
```

**2. Get an access token for each account.** The token must be a *user* token for the audience `https://api.foc.local/chat-service`. A token from the Auth0 dashboard's API "Test" tab will **not** work: that is a machine-to-machine token, and chat routes refuse those with `403`. One way is Auth0's password grant, using an Auth0 application that has the "Password" grant type enabled (Applications → your app → Advanced Settings → Grant Types). A separate test application is the safest choice:

```sh
curl -s https://<your-tenant>/oauth/token -H 'Content-Type: application/json' -d '{
  "grant_type": "http://auth0.com/oauth/grant-type/password-realm",
  "realm": "Username-Password-Authentication",
  "username": "<test user email>", "password": "<password>",
  "audience": "https://api.foc.local/chat-service",
  "client_id": "<application client id>", "client_secret": "<secret, if the app has one>"
}'
```

Copy `access_token` from the answer. To see the user's ID, paste the token into <https://jwt.io> and read the `sub` field (for example `auth0|6543...`). Check that `permissions` contains `chats:use`; if not, the user's role is missing it.

**3. Insert a room by hand** with the two users' `sub` values:

```sh
docker compose exec chat-db psql -U chat_service -d chat_service -c "
  INSERT INTO chat_rooms (errand_id, requester_id, courier_id, opened_version)
  VALUES (gen_random_uuid(), '<requester sub>', '<courier sub>', 1)
  RETURNING id;"
```

**4. Chat** (`ROOM` is the ID printed above):

```sh
curl -s -X POST localhost:3006/chats/$ROOM/messages -H "Authorization: Bearer $REQUESTER_TOKEN" \
  -H 'Content-Type: application/json' -d '{"clientMsgId":"test-1","body":"I am at the lobby"}'
curl -s localhost:3006/chats/$ROOM/messages -H "Authorization: Bearer $COURIER_TOKEN"
curl -s localhost:3006/me/chats -H "Authorization: Bearer $COURIER_TOKEN"
```

Things worth trying: send the same `clientMsgId` twice (the second answer is `200`, and only one message is stored); read with a third user's token (`404`); call without a token (`401`). To see a closed room, end it by hand and then try to send (`409 ROOM_CLOSED`):

```sql
UPDATE chat_rooms SET ended_at = now() - interval '2 days', end_reason = 'COMPLETED',
                      closes_at = now() - interval '1 day' WHERE id = '<room id>';
```

## Watching events in RabbitMQ

RabbitMQ has a web interface at <http://localhost:15672> (user `foc`, password `foc-local-dev`, unless the root `.env` changes them). Under **Queues and Streams**:

- `chat-service.errand-events` shows events waiting for chat-service. It is normally at 0, because they are processed straight away.
- `chat-service.errand-events.dlq` holds events chat-service could not apply. Open it and use **Get messages** to see why. The chat-service log says why too, with the same message ID.

**The phase 4 check.** Accept an errand in the app (<http://localhost:8081>) as a courier, then look for the room:

```sh
docker compose exec chat-db psql -U chat_service -d chat_service \
  -c "SELECT id, errand_id, courier_id, created_at FROM chat_rooms ORDER BY created_at DESC LIMIT 5;"
docker compose logs chat-service | grep "errand.accepted applied"
```

**Without the frontend**, publish an `errand.accepted` event yourself, exactly as order-service would: in the management UI open **Exchanges → foc.events → Publish message**, set the routing key to `errand.accepted`, and paste this payload (replace both errand IDs with one new UUID):

```json
{"messageId":"<errand-id>:accepted:3","type":"errand.accepted","occurredAt":"2026-11-09T14:00:00Z",
 "errandId":"<errand-id>","errandVersion":3,
 "data":{"errandId":"<errand-id>","errandVersion":3,"event":"accept","from":"OPEN","to":"ASSIGNED",
         "actorType":"COURIER","actorId":"auth0|courier","requesterId":"auth0|requester",
         "courierId":"auth0|courier","reward":5,"at":"2026-11-09T14:00:00Z"}}
```

Publish it twice and there is still one room. Publish `{not json` and it lands in the dead-letter queue.

## Trying live chat

**In the app.** With the stack running (`docker compose up --build` from the repository root), sign in at <http://localhost:8081> as one user in one browser and as another user in a second browser (or a private window). Request an errand as the first, accept it as the second, and open the errand on both sides: a **Chat** section appears under the action buttons. Messages appear on the other side straight away, and survive a refresh. For `npm run dev` (port 5173), run chat-service in Compose as well; Vite forwards `/chat-service` to `localhost:3006`. The SPA asks Auth0 for a token for `https://api.foc.local/chat-service` with `chats:use`, so the [Auth0 set-up](#auth0-set-up) must be done first.

**From the browser console**, without the panel:

You need two user tokens for the chat API (see [Trying the API by hand](#trying-the-api-by-hand), step 2) and a room the two users share (accept an errand in the app, or insert one by hand).

Open two browser tabs on `http://localhost:8081` (an allowed origin), open the developer console in each, and paste, with each user's own token:

```js
const ws = new WebSocket("ws://localhost:3006/ws");
ws.onmessage = (e) => console.log(JSON.parse(e.data));
ws.onclose = (e) => console.log("closed", e.code, e.reason);
ws.onopen = () => ws.send(JSON.stringify({ type: "auth", token: "<this user's access token>" }));
```

Each tab prints `{type: "ready", ...}`. Then send from one tab:

```js
ws.send(JSON.stringify({ type: "send", roomId: "<room id>", clientMsgId: crypto.randomUUID(), body: "hello" }));
```

The sending tab prints `ack` and `message`; the other tab prints `message` straight away. A command-line alternative is `websocat ws://localhost:3006/ws`, typing each frame as one line. Accepting an errand in the app while both tabs are connected prints `room_opened` in both.

## Trying room closing

Make the 24 hours a minute: add `CHAT_CLOSE_DELAY=1m` to `chat-service/.env` and restart chat-service (`docker compose up -d chat-service` from the repository root).

1. In two browsers, request an errand as one user and accept it as the other, as in [Trying live chat](#trying-live-chat). Chat a little.
2. As the courier, mark it picked up and delivered; as the requester, confirm the delivery. Both panels show the "This errand is complete …" banner straight away, and sending still works.
3. A minute after the confirmation, the chat section disappears from both pages, without a refresh, and stays gone after a refresh. The room and its messages are still in the database (query below).
4. **Withdrawal:** accept another errand, then withdraw as the courier. Both see the banner; the withdrawn courier still finds the room on the errand's page. Accept it with a third user: the requester now has two conversations, the new one open and empty, the old one closing. A minute later the old one disappears from the list.

To check the database: `docker compose exec chat-db psql -U chat_service -d chat_service -c "SELECT courier_id, end_reason, ended_at, closes_at FROM chat_rooms ORDER BY created_at DESC LIMIT 5;"`

Remove `CHAT_CLOSE_DELAY` from `.env` afterwards. A shorter delay applies only to endings processed while it is set; rooms that already have a `closes_at` keep it.

## Auth0 set-up

Do this once per Auth0 tenant, in the dashboard:

1. **Applications → APIs → Create API**: identifier `https://api.foc.local/chat-service`, signing algorithm RS256.
2. In its **Settings**, turn on **Enable RBAC** and **Add Permissions in the Access Token**.
3. In its **Permissions** tab, add `chats:use` (read and send in your own chats).
4. **User Management → Roles**: give `chats:use` to the user role and to the administrator role (the roles user-service already assigns).

## Working with the database

**Adding a migration.** Never edit a migration that has been applied anywhere shared; add the next numbered file instead (`00002_<what>.sql`, with `-- +goose Up` and `-- +goose Down`). Then:

```sh
go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate   # regenerate Go code
docker compose run --rm chat-migrate                         # from the repo root
```

**Changing a query.** Edit `queries/chat.sql`, run `sqlc generate`, then add or update the store method that calls it.

**Looking at the tables.** `chat-db` sits on a private Docker network with no port open to your machine, like the other services' databases. To look inside:

- From the terminal: `docker compose exec chat-db psql -U chat_service -d chat_service`, then `\dt` lists the tables.
- With DBeaver or another GUI: migrate the test database and connect to it at `127.0.0.1:5435`, database `chat_test`, user `chat_test`, no password:
  ```sh
  docker compose -f compose.test.yaml up -d
  GOOSE_DRIVER=postgres GOOSE_DBSTRING='postgres://chat_test@127.0.0.1:5435/chat_test?sslmode=disable' \
    go run github.com/pressly/goose/v3/cmd/goose@v3.29.0 -dir migrations up
  ```
  The tables are in the `public` schema. Schemas named `chat_test_<random>` that appear while tests run are deleted when the tests finish.

## Environment variables

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `DATABASE_URL` | Yes | | PostgreSQL URL. On RDS add `sslmode=verify-full&sslrootcert=/etc/ssl/certs/rds-global-bundle.pem` |
| `TEST_DATABASE_URL` | When `APP_ENV=test` | | Test database URL, used instead of `DATABASE_URL` |
| `AMQP_URL` | Yes | | RabbitMQ URL (`amqp://` or `amqps://`) |
| `AMQP_PREFETCH` | No | `10` | Events RabbitMQ sends ahead of acknowledgements (1 to 1000) |
| `TEST_AMQP_URL` | No | | Throwaway RabbitMQ for integration tests |
| `AUTH0_ISSUER` | Yes | | Auth0 tenant, e.g. `https://login.bestfoc.app/` |
| `AUTH0_AUDIENCE` | No | `https://api.foc.local/chat-service` | Auth0 API identifier |
| `AUTH0_CLOCK_SKEW` | No | `30s` | Tolerance for clock differences when checking token times (0s to 1m) |
| `AUTH0_JWKS_*` | No | as order-service | Signing-key cache: `CACHE_TTL` `5m`, `FETCH_TIMEOUT` `3s`, `REFRESH_INTERVAL` `10s`, `FETCH_ATTEMPTS` `1`, `BREAKER_THRESHOLD` `3`, `BREAKER_COOLDOWN` `30s` |
| `WS_ALLOWED_ORIGINS` | Yes | | Comma-separated host patterns allowed to open a WebSocket, e.g. `localhost:8081,localhost:5173`; production `app.bestfoc.app` |
| `APP_ENV` | No | `development` | `development`, `test` or `production` |
| `HTTP_HOST` / `HTTP_PORT` | No | `0.0.0.0` / `3006` | Listen address |
| `LOG_LEVEL` | No | `info` | `debug`, `info`, `warn`, `error` |
| `STARTUP_TIMEOUT` / `SHUTDOWN_TIMEOUT` | No | `30s` / `10s` | |
| `DATABASE_POOL_MIN` / `DATABASE_POOL_MAX` | No | `1` / `10` | |
| `CHAT_CLOSE_DELAY` | No | `24h` | How long a room stays writable after its errand ends. `1m` for manual testing |
| `CHAT_MAX_MESSAGE_LENGTH` | No | `2000` | 1 to 2000 (the database limit) |
| `CHAT_SEND_RATE` / `CHAT_SEND_BURST` | No | `5` / `10` | Per-connection send limit: messages per second, and burst |
| `WS_AUTH_TIMEOUT` | No | `5s` | Time allowed for the first `auth` frame |
| `WS_PING_INTERVAL` | No | `25s` | Must be under 60 s, the ALB idle timeout |
| `WS_MAX_CONNECTIONS_PER_USER` | No | `5` | 1 to 100 |
| `REDIS_URL` | No | empty | Empty: in-process broadcaster. Set when running more than one copy |

Configuration errors list every problem at once and never include a value.

## Tests

```sh
go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate   # once, and after changing queries or migrations
gofmt -l . && go vet ./... && go test ./... && go build ./...
```

Without PostgreSQL and RabbitMQ, the tests that need them skip themselves. To run them too:

```sh
docker compose -f compose.test.yaml up -d
TEST_DATABASE_URL='postgres://chat_test@127.0.0.1:5435/chat_test?sslmode=disable' \
TEST_AMQP_URL='amqp://guest:guest@127.0.0.1:5674/' go test -p 1 ./...
```

Use the test RabbitMQ (port 5674), never `queue-service`: the tests delete and recreate chat-service's queues. CI runs all of them against its own PostgreSQL and RabbitMQ.
