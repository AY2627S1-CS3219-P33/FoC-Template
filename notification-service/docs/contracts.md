<!--
AI Assistance Disclosure:
Tool: Claude, date: 2026-11-11
Scope: Notification-service API and event contract documentation.
Author review: I validated correctness, edited for style, and added boundary checks.
-->

# notification-service contracts

This service implements the notification half of the messaging contract in
[../../docs/asyncapi.yaml](../../docs/asyncapi.yaml) and the HTTP contract in
[../openapi.yaml](../openapi.yaml). It consumes errand events and never
publishes.

## Consuming

- **Topology.** A durable quorum queue `notification-service.errands` is bound
  to `errand.#` on the durable topic exchange `foc.events`. A failed delivery
  is retried up to 5 times, then dead-lettered to
  `notification-service.errands.dlq`. The service declares the exchange, the
  queue and the binding on every (re)connect.
- **Delivery.** At-least-once and possibly reordered. Each event carries a
  stable `messageId`; a single event can notify several users, so one row per
  `(messageId, recipient)` is stored and that pair is unique. A redelivery
  therefore stores nothing new. The delivery is acknowledged only after the
  rows have committed.
- **Undecodable messages** (bad JSON, a `type` that does not match the routing
  key, a missing id, or an unrecognised `errand.*` key) are rejected without
  requeue so they are dead-lettered rather than retried forever.

## Event → notification mapping

Recipients come only from the event payload (`requesterId`, `courierId`,
`actorId`); contact names and mobile numbers are never published and so are
never read. A party is never notified about an action it performed itself (the
`actorId` is removed from the recipients when `actorType` is `REQUESTER` or
`COURIER`).

| Event | Notifies | Note |
| --- | --- | --- |
| `errand.placed` | — | Internal: not yet an available errand. |
| `errand.opened` | requester | The errand is live. |
| `errand.rejected` | requester | Reservation failed; not posted. |
| `errand.edited` / `errand.edit_requested` / `errand.edit_rejected` | requester | Reward-edit lifecycle. |
| `errand.accepted` | requester | A courier accepted (the courier is the actor). |
| `errand.cancelled` | requester + courier | Minus whoever cancelled. |
| `errand.withdrawn` | requester | A courier withdrew. |
| `errand.expired` | requester + courier | |
| `errand.picked_up` | requester | The courier picked it up. |
| `errand.delivered` | requester | Confirm to release the reward. |
| `errand.review_required` | requester + courier | Awaiting admin review. |
| `errand.completed` / `errand.refunded` | requester + courier | Settlement outcome. |
| `errand.accept_rejected` / `errand.cancel_rejected` / `errand.pickup_rejected` | the actor | Their action could not be applied. |
| `errand.verification_reminder` | requester | Confirm before the deadline. |
| `errand.pickup_deadline_approaching` | courier | Pick up before the deadline. |

A recognised event with no eligible recipient (for example a cancellation of an
errand that no courier had accepted) is acknowledged without storing anything.

## Serving

Every HTTP route resolves the recipient from the verified token's subject and
takes no user identifier from the request, so a user can only ever read or
change their own notifications. `createdAt` is the event's `occurredAt`, so the
order a user sees matches the order things happened rather than the order the
broker delivered them.

## Pushing the unread count

`GET /v1/notifications/stream` is the push side: it holds the connection open
and sends the caller's unread count as Server-Sent Events — once on connect,
then whenever a notification is recorded for that user — so clients show a live
badge without polling. When the consumer stores a notification it wakes an
in-process hub ([internal/notifstream](../internal/notifstream)), which signals
the recipient's open streams; each stream then reads the authoritative count
from the database and writes an `event: unread` frame. Signals coalesce and
carry no data, so a burst of events costs one refresh. The hub is per-instance,
so events and a user's stream must reach the same instance to push immediately;
the per-stream heartbeat and the client's reconnect keep the badge eventually
correct across instances, and the count itself is always read from the shared
database. The token is sent in the Authorization header, so nothing sensitive
is placed in the URL.
