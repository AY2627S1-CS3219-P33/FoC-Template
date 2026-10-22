# Credit-service contracts

This document records the rules credit-service shares with other services:
the HTTP API for user-service and users, and the RabbitMQ messages exchanged
with order-service. It follows the layout of
[order-service/docs/contracts.md](../../order-service/docs/contracts.md); the
message shapes here must match that file and
[docs/asyncapi.yaml](../../docs/asyncapi.yaml). The data model is in
[wallet-aggregate.md](wallet-aggregate.md).

The machine-readable contracts are [openapi.yaml](../openapi.yaml) for the HTTP
API and the shared [docs/asyncapi.yaml](../../docs/asyncapi.yaml) for
messages. This file explains the rules behind them.

Status of each part:

| Part | Status |
| --- | --- |
| HTTP API | built |
| Command rules (`internal/wallet/commands.go`) | built, tested |
| Message layer (`internal/creditmsg`), `outbox` table, results written in the command transaction | built, tested |
| RabbitMQ consumer and topology (`creditmsg.Consumer`, `DeclareTopology`) | built, tested against a real broker; not started by `main` yet |
| Outbox worker (publishes the results) | **proposed**, not built |

Proposed parts need the agreement of the order-service owner (gate C1 in
order-service's contracts) before they are final.

## Contents

1. [Authorization and errors](#authorization-and-errors)
2. [HTTP routes](#http-routes)
3. [Credit amounts](#credit-amounts)
4. [Message IDs and idempotency](#message-ids-and-idempotency)
5. [RabbitMQ contract](#rabbitmq-contract)
6. [Envelope](#envelope)
7. [Commands](#commands)
8. [Results](#results)
9. [Result outbox](#result-outbox)
10. [Consumer behaviour](#consumer-behaviour)

## Authorization and errors

Every route except `/readyz` needs an Auth0 RS256 access token. Two kinds of
caller exist, and the route decides which kind it accepts:

| Caller | Token | Routes |
| --- | --- | --- |
| User | login token | `/v1/wallet/*` |
| Service | machine-to-machine token (`gty=client-credentials`) | `/internal/*` |

A user token on a service route is refused, even if it carries a service
permission. Permissions come from the token's `permissions` claim only. The
identity of a user comes from the verified token, never from the request.

Errors have one JSON shape:

```json
{"code": "WALLET_NOT_FOUND", "message": "wallet not found"}
```

| HTTP status | `code` | When |
| --- | --- | --- |
| 400 | `INVALID_ARGUMENT` | bad owner ID, bad subject, bad body |
| 401 | `UNAUTHENTICATED` | missing or invalid token |
| 403 | `FORBIDDEN` | wrong caller kind or missing permission |
| 404 | `WALLET_NOT_FOUND` | no wallet for this owner or token |
| 409 | `WALLET_CONFLICT` | owner already has a different Auth0 subject, or the subject belongs to another owner |
| 503 | `DEPENDENCY_UNAVAILABLE` | the token could not be verified because Auth0 is not reachable |
| 500 | `INTERNAL` | anything else; details are never shown |

## HTTP routes

| Route | Caller | Permission | Result |
| --- | --- | --- | --- |
| `GET /v1/wallet/me` | user | `credits:read:own` | the caller's wallet, found by the token subject |
| `GET /internal/wallets/{ownerId}` | service | `credits:read` | a wallet by owner UUID |
| `PUT /internal/wallets/{ownerId}` | service | `credits:wallets:create` | creates the wallet and the initial allocation |

`PUT` takes `{"auth0Subject": "auth0|abc"}`. It is idempotent: `201` when the
wallet is created, `200` with the same wallet when it already exists. The
initial allocation is configuration (`INITIAL_CREDIT_AMOUNT`); a caller cannot
choose it.

Wallet response:

```json
{"ownerId": "00000000-0000-0000-0000-00000000a11c", "available": "100",
 "reserved": "0", "createdAt": "2026-10-14T09:00:00Z"}
```

There is no HTTP route for reserve, adjust, release or transfer. Those only
arrive as RabbitMQ commands.

## Credit amounts

Credits are whole numbers (`bigint`). In JSON bodies of HTTP responses they
are strings, so no client loses precision. In RabbitMQ messages they are JSON
numbers, as order-service sends them.

## Message IDs and idempotency

Every message has a `messageId`. It is the key for idempotency.

- A **command** `messageId` is chosen by order-service. It is the outbox row's
  key: `{errandId}:reserve`, `{errandId}:release`, `{errandId}:transfer`,
  `{errandId}:adjust:{version}`, `{errandId}:adjust-revert:{newVersion}`. It
  does not change when order-service re-sends the command.
- A **result** `messageId` is `{command messageId}:{result type}`, for example
  `E:reserve:credit.reserved`. This is **proposed**; order-service's contract
  says the same. The result's `correlationId` is the command's `messageId`.

Rules:

1. Credit-service stores the result of every command it applies, keyed by the
   command's `messageId` (`processed_commands`), in the same transaction as
   the balance change.
2. A repeated command gets the stored result. No balance changes. This is also
   true for refusals: a refused reserve stays refused even if the balance
   grows later. A `credit.operation_failed` result is the exception: it is not
   stored, so a repeated command is judged anew. An administrator can then fix
   the cause and send the same command again.
3. The balance changes are also protected by the ledger: each ledger entry has
   a key that is unique per wallet (`reserve:{orderId}`,
   `release:{orderId}`, `transfer:{orderId}`, `adjust:{orderId}:{seq}`).
4. Result messages are idempotent for the receiver: a re-published result has
   the same `messageId`, and order-service drops duplicates.

## RabbitMQ contract

Local RabbitMQ only for now. The broker is the `rabbitmq` container in the
root `compose.yaml`.

| Exchange | Type | Durable | Credit-service uses it to |
| --- | --- | --- | --- |
| `foc.commands` | direct | yes | receive commands |
| `foc.events` | topic | yes | publish results |

| Queue | Owner | Binding | Notes |
| --- | --- | --- | --- |
| `credit-service.commands` | credit-service | `foc.commands`: `credit.reserve`, `credit.adjust`, `credit.release`, `credit.transfer` | quorum, durable; dead-letters to `credit-service.commands.dlq` |
| `credit-service.commands.dlq` | credit-service | the default exchange | quorum; holds commands that cannot be processed |

Credit-service declares both exchanges (declaring is safe to repeat), its own
queue, its binding and its dead-letter queue when it starts. It never
declares `order-service.credit-results`.

Publishing results: persistent messages (`delivery_mode` 2) on a channel in
confirm mode, with the `mandatory` flag. Publishing returns only after the
broker confirms. AMQP properties: `message_id` = `messageId`, `type` = the
routing key, `correlation_id` = `correlationId`, `content_type` =
`application/json`, `timestamp` = `occurredAt`. The routing key of a result is
its type, for example `credit.reserved`.

## Envelope

Every message body, in both directions, is this JSON object:

```json
{
  "messageId": "6f1c2b0e-8d4a-4e1b-9c3f-2a7d5e8b9c10:reserve:credit.reserved",
  "correlationId": "6f1c2b0e-8d4a-4e1b-9c3f-2a7d5e8b9c10:reserve",
  "type": "credit.reserved",
  "occurredAt": "2026-10-14T09:00:01Z",
  "errandId": "6f1c2b0e-8d4a-4e1b-9c3f-2a7d5e8b9c10",
  "errandVersion": 1,
  "data": {"amount": 5}
}
```

| Field | Meaning |
| --- | --- |
| `messageId` | See [Message IDs](#message-ids-and-idempotency). |
| `correlationId` | Results only: the `messageId` of the command answered. |
| `type` | The routing key. |
| `occurredAt` | When the result was created (the time of the transaction), not when it was published. |
| `errandId`, `errandVersion` | Copied from the command. |
| `data` | The command data, or the result data. |

## Commands

Order-service → credit-service. Routing key = the command name. Each command's
`data` always contains `errandId` and `errandVersion`, plus these fields.
Users are Auth0 subjects, for example `auth0|req1`.

| Command | `data` fields | Code function |
| --- | --- | --- |
| `credit.reserve` | `requesterId`, `amount` | `Service.Reserve` |
| `credit.adjust` | `requesterId`, `newAmount`, `seq` | `Service.Adjust` |
| `credit.release` | `requesterId` | `Service.Release` |
| `credit.transfer` | `requesterId`, `courierId`, `amount` | `Service.Transfer` |

Rules for each command:

**`credit.reserve`.** Moves `amount` from `available` to `reserved` and
creates the reservation of the errand.
- Refused with `RESERVATION_RELEASED` if a release for this errand came first
  (see the tombstone below).
- Refused with `ACCOUNT_NOT_FOUND` if the requester has no wallet.
- Refused with `INSUFFICIENT_CREDITS` if `available` is less than `amount`.
- If the errand already has a reservation, the answer is `credit.reserved`
  with the current amount.

**`credit.adjust`.** Changes the amount of an active reservation to
`newAmount`. `newAmount` is absolute, not a difference.
- `seq` must be higher than the last applied `seq` of this reservation. If it
  is not, the command is acknowledged with `credit.adjusted` and the current
  amount, and nothing changes. This makes a revert safe even if it overtakes
  the edit it cancels.
- A refused adjust does not use up its `seq`.
- Refused with `RESERVATION_NOT_ACTIVE` if there is no reservation, or it is
  released or transferred.
- Refused with `INSUFFICIENT_CREDITS` if the increase is more than
  `available`.
- `seq` values: an edit made against version `v` sends `2v`; a revert by the
  transition to version `w` sends `2w − 1`. Credit-service only needs them to
  increase.

**`credit.release`.** Moves the whole reservation back to `available`.
- Answer is `credit.released` also when the reservation was already released.
- If the errand has no reservation, credit-service records a **tombstone**
  (`released_orders`) and answers `credit.released`. A reserve for the same
  errand that arrives later is refused with `RESERVATION_RELEASED`. This
  covers a release that overtakes its reserve.
- Fails with `credit.operation_failed` (`RESERVATION_ALREADY_TRANSFERRED`) if
  the credits were already paid to the courier.

**`credit.transfer`.** Pays the courier `amount`. Both wallets change in one
transaction.
- `amount` is the errand's reward in order-service, so it decides what the
  courier gets. It is paid from the requester's reservation first.
- If the reservation holds more, the surplus returns to the requester's
  `available` balance. This happens when an adjust that order-service dropped
  (the courier accepted while an edit was pending) has not been reverted yet.
  The late revert is then refused with `RESERVATION_NOT_ACTIVE`, which
  order-service ignores.
- If the reservation holds less, the shortfall is taken from the requester's
  `available` balance. This happens when a lowered edit was dropped and its
  revert was refused because the requester had spent the freed credits. If
  `available` cannot cover the shortfall, nothing moves and the result is
  `credit.operation_failed` (`INSUFFICIENT_CREDITS`).
- Answer is `credit.transferred` also when it was already transferred.
- Fails with `credit.operation_failed` if the reservation was released
  (`RESERVATION_ALREADY_RELEASED`, also for a tombstone), does not exist
  (`RESERVATION_NOT_FOUND`), or the requester or courier has no wallet
  (`ACCOUNT_NOT_FOUND`).

## Results

Credit-service → order-service, on `foc.events`. The result `data` holds only
the fields below. Absent fields are omitted, never `null`.

| Command | Success | Refusal or failure |
| --- | --- | --- |
| `credit.reserve` | `credit.reserved` `{amount}` | `credit.reservation_refused` `{code}`: `INSUFFICIENT_CREDITS`, `ACCOUNT_NOT_FOUND`, `RESERVATION_RELEASED` |
| `credit.adjust` | `credit.adjusted` `{amount, seq}` | `credit.adjust_refused` `{code, seq}`: `INSUFFICIENT_CREDITS`, `RESERVATION_NOT_ACTIVE` |
| `credit.release` | `credit.released` `{}` | `credit.operation_failed` `{code}`: `RESERVATION_ALREADY_TRANSFERRED` |
| `credit.transfer` | `credit.transferred` `{}` | `credit.operation_failed` `{code}`: `INSUFFICIENT_CREDITS`, `RESERVATION_ALREADY_RELEASED`, `RESERVATION_NOT_FOUND`, `ACCOUNT_NOT_FOUND` |

`credit.operation_failed` means a state that should be impossible, or a
contract bug. Order-service marks its outbox row `FAILED` for an admin and
does not retry it. `RESERVATION_NOT_FOUND` is **not** in the
`OperationFailedData` code list of `docs/asyncapi.yaml` yet (the other codes
are). Order-service only stores the code, so adding it should not change its
behaviour. Agree this with its owner and add it to the list.

Samples, for errand `E`:

```json
{"messageId": "E:reserve:credit.reservation_refused", "correlationId": "E:reserve",
 "type": "credit.reservation_refused", "occurredAt": "2026-10-14T09:00:01Z",
 "errandId": "E", "errandVersion": 1,
 "data": {"code": "INSUFFICIENT_CREDITS"}}
```

```json
{"messageId": "E:adjust:2:credit.adjusted", "correlationId": "E:adjust:2",
 "type": "credit.adjusted", "occurredAt": "2026-10-14T09:05:01Z",
 "errandId": "E", "errandVersion": 2,
 "data": {"amount": 8, "seq": 4}}
```

```json
{"messageId": "E:transfer:credit.transferred", "correlationId": "E:transfer",
 "type": "credit.transferred", "occurredAt": "2026-10-14T10:15:01Z",
 "errandId": "E", "errandVersion": 7, "data": {}}
```

```json
{"messageId": "E:release:credit.operation_failed", "correlationId": "E:release",
 "type": "credit.operation_failed", "occurredAt": "2026-10-14T10:20:01Z",
 "errandId": "E", "errandVersion": 8,
 "data": {"code": "RESERVATION_ALREADY_TRANSFERRED"}}
```

## Result outbox

Credit-service must not lose a result after it changes a balance. So a
command does not publish its result directly. In the same transaction as the
balance change it inserts a row in the `outbox` table (built:
`creditmsg.OutboxRecorder`). A separate worker publishes the rows (proposed,
not built). If a command is delivered again, its stored result is queued again
unless the row is still `PENDING`; this is how a lost result is recovered.

- Row key = the result `messageId`; the payload is the full envelope. The key
  is unique, so a repeated command cannot create a second row.
- Row lifecycle: `PENDING` → leased by a worker → `SENT` after the broker
  confirms. A failed publish is retried with growing delay; after the maximum
  attempts the row becomes `FAILED`.
- Several workers can run. A row is claimed with `FOR UPDATE SKIP LOCKED` and
  a lease, so two workers never publish the same row at the same time.
- If a worker crashes after publishing, the row is published again with the
  same `messageId`. This is safe because order-service drops duplicates.
- Rows are not deleted for now. A cleanup of old `SENT` rows can come later.

Credit-service needs no `CONFIRMED` state, because order-service does not
confirm results.

## Consumer behaviour

For each message from `credit-service.commands`, `creditmsg.Consumer` calls
`Handler.Handle` and settles the message by `DispositionOf(err)`:

| Case | Action |
| --- | --- |
| Command applied, or a stored result replayed; result row is in the outbox | `ack` |
| Unknown routing key or `type`, body that is not valid JSON, `ErrInvalid`, or `ErrConflict` | `nack` without requeue, so it goes to the dead-letter queue |
| Database or other temporary error | wait with backoff, then `nack` with requeue |
| Temporary error on the 5th attempt | `nack` without requeue (dead-letter) |

The consumer acks only after the transaction has committed. It handles one
message at a time (prefetch 16 only buffers). A panic in the handler counts as
a temporary error. Credit-service does not rely on the order of messages: the
tombstone and `seq` rules make late and repeated messages safe.

**Attempt limit.** RabbitMQ 4.4 does not count a message that is requeued with
`basic.nack` against the queue's `x-delivery-limit`; such a message is
delivered again for ever. So the consumer counts attempts itself, from the
`x-acquired-count` header that the broker sets on a redelivery, and
dead-letters on the 5th. The header is kept by the broker, so the count
survives a restart and is shared by replicas. A dead-lettered command is not
lost: order-service sends a command again if it gets no result in 10 minutes,
and a repeat gets the stored result.
