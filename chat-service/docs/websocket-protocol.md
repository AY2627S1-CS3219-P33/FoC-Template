<!--
AI Assistance Disclosure:
Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-10
Scope: Documentation writing and formatting.
Author review: Reviewed, validated, and tested by the authors.
-->

# chat-service WebSocket protocol

This is the contract between a browser and `GET /ws`. The browser client is [useChatSocket.ts](../../user-frontend/src/chat/useChatSocket.ts) in user-frontend. The REST routes are in [../openapi.yaml](../openapi.yaml); the frames here use the same room and message shapes and the same error codes. The Go definitions are in [internal/protocol](../internal/protocol/protocol.go).

## Connecting

| Where | URL |
|---|---|
| Production | `wss://app.bestfoc.app/chat-service/ws` |
| Local, through nginx | `ws://localhost:8081/chat-service/ws` |
| Local, through the Vite dev server | `ws://localhost:5173/chat-service/ws` |
| Local, direct | `ws://localhost:3006/ws` |

The page's origin must be in `WS_ALLOWED_ORIGINS`; otherwise the handshake is refused with HTTP `403`. Tools that send no `Origin` header (such as `websocat`) may connect, but still need a token.

Every frame is one JSON object in a **text** message, with a `type` field. Frames larger than 16 KiB close the connection.

## 1. Authenticate first

Within **5 seconds** (`WS_AUTH_TIMEOUT`) of connecting, send:

```json
{"type": "auth", "token": "<Auth0 access token>"}
```

The token must be a user token for the audience `https://api.foc.local/chat-service` with the `chats:use` permission. The token goes in a frame because browsers cannot set an `Authorization` header on a WebSocket, and a token in the URL would end up in server logs.

On success the server answers:

```json
{"type": "ready", "userId": "auth0|6543..."}
```

**After `ready`, catch up over REST.** Messages sent while you were disconnected are not replayed over the socket. For each open room, call `GET /chats/{roomId}/messages?after=<last message id you have>`. Do the same after every reconnect.

## 2. Send a message

```json
{"type": "send", "roomId": "5e1f...", "clientMsgId": "6f1c2a52-8d0e-4b8a-9f5e-0d3c1b7a2e44", "body": "I'm at the lobby"}
```

- `clientMsgId`: make a new UUID (`crypto.randomUUID()`) for each message, and **reuse it when retrying** the same message. The server stores a message once per `clientMsgId` and room, so a retry never creates a duplicate. Allowed characters: letters, digits and `. _ : -`, 1 to 64 of them.
- `body`: 1 to 2000 characters after trimming spaces at both ends. Newlines and tabs are allowed; other control characters are not. It is stored exactly as typed, so **display it as text, never as HTML**.

You get exactly one answer for each `send`, matched by `clientMsgId`:

```json
{"type": "ack", "clientMsgId": "6f1c2a52-...", "message": { ...Message... }}
```

```json
{"type": "error", "code": "ROOM_CLOSED", "message": "this chat is closed; messages can no longer be sent", "clientMsgId": "6f1c2a52-..."}
```

Separately, a `message` frame (below) reaches **both** participants, including this tab and the sender's other tabs. Use the message `id` to drop duplicates: the same message can arrive as an `ack`, a `message` frame and a REST catch-up.

At most 5 sends per second per connection, with bursts of up to 10 (`CHAT_SEND_RATE`, `CHAT_SEND_BURST`). Sends over the limit get `error` with code `RATE_LIMITED`; wait a moment and retry with the same `clientMsgId`.

## 3. Frames from the server

| `type` | Fields | When |
|---|---|---|
| `ready` | `userId` | Authenticated. Catch up over REST now |
| `message` | `message` | A new message in one of your rooms, from either participant |
| `ack` | `clientMsgId`, `message` | Your `send` was saved |
| `error` | `code`, `message`, optional `clientMsgId` | A frame was refused |
| `room_opened` | `room` | A courier accepted one of your errands, or you accepted one |
| `room_closing` | `roomId`, `errandId`, `endReason`, `closesAt` | The errand ended for that room. It stays writable until `closesAt`, then becomes read-only |

`message` and `room` have exactly the shapes of the REST API's `Message` and `Room` ([../openapi.yaml](../openapi.yaml)):

```json
{"type": "message", "message": {
  "id": 57, "roomId": "5e1f...", "senderId": "auth0|xyz", "senderRole": "COURIER",
  "body": "On my way up", "clientMsgId": "...", "createdAt": "2026-11-09T14:05:40Z"}}
```

```json
{"type": "room_opened", "room": {
  "id": "5e1f...", "errandId": "7b0c...", "requesterId": "auth0|abc", "courierId": "auth0|xyz",
  "myRole": "REQUESTER", "status": "OPEN", "endedAt": null, "endReason": null,
  "closesAt": null, "createdAt": "2026-11-09T14:02:00Z"}}
```

There is no "room closed" frame. The browser already knows `closesAt` and switches the room to read-only at that time with its own timer. If the browser's clock is wrong, the server's `ROOM_CLOSED` error on the next send corrects it.

### Error codes

The same codes as the REST API:

| `code` | Meaning |
|---|---|
| `INVALID_ARGUMENT` | Malformed frame field (e.g. `roomId` is not a UUID), unknown `type`, or a second `auth` |
| `INVALID_MESSAGE` | The body or `clientMsgId` breaks a rule; `message` says which |
| `ROOM_NOT_FOUND` | No such room, or you are not one of its two participants |
| `ROOM_CLOSED` | The room is read-only |
| `RATE_LIMITED` | Too many sends; slow down |
| `DEPENDENCY_UNAVAILABLE` | The database is unavailable; retry later |
| `INTERNAL` | Unexpected server error; retry later |

## 4. Closing and reconnecting

The server pings every 25 seconds (`WS_PING_INTERVAL`); browsers answer automatically. A connection that stops answering is closed.

| Close code | Meaning | What the browser should do |
|---|---|---|
| `1000` | Normal close | Nothing |
| `1006` | Connection lost (no close frame; network, laptop asleep) | Reconnect with backoff |
| `1008` | A malformed frame (not JSON text), or the tab was too slow to receive its frames | Reconnect, then catch up over REST |
| `1012` | The service is restarting (deploy) | Reconnect straight away |
| `1013` | Auth0 is unreachable, so the token could not be checked | Reconnect with backoff |
| `4401` | No valid `auth` frame within 5 s, an invalid token, or the token expired | Get a fresh token, then reconnect |
| `4403` | Valid token, but not a user token or missing `chats:use` | Do not retry; the account cannot use chat |
| `4429` | Too many open connections for this user (5, `WS_MAX_CONNECTIONS_PER_USER`) | Close other tabs; retry later |

Suggested backoff: 1 s, 2 s, 5 s, then up to 30 s, with some randomness so many browsers don't reconnect at the same moment. After every `ready`, catch up over REST.

## Example (browser console)

```js
const ws = new WebSocket("ws://localhost:3006/ws");
ws.onmessage = (e) => console.log(JSON.parse(e.data));
ws.onclose = (e) => console.log("closed", e.code, e.reason);
ws.onopen = () => ws.send(JSON.stringify({ type: "auth", token: "<access token>" }));
// after "ready":
ws.send(JSON.stringify({ type: "send", roomId: "<room id>", clientMsgId: crypto.randomUUID(), body: "hello" }));
```

The page's origin must be allowed, so run this from a tab on `http://localhost:8081` or `http://localhost:5173`. Or use `websocat ws://localhost:3006/ws` and type the frames as single lines.
