// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: User-frontend API client for notification service data.
// Author review: I validated correctness, edited for style, and added boundary checks.

// Notification Service client.
//
// Mirrors notification-service/openapi.yaml (v1.1.0). Every route is scoped to
// the caller by their token: there is no way to read or change another user's
// notifications, and notifications are created only by the service from errand
// events, never over HTTP. The list is keyset-paginated newest-first; pass the
// nextCursor from one page to fetch the next. The unread badge is delivered by
// server push over the stream route (streamUnread), not by polling.

const BASE = "/notification-service";

export type NotificationErrorCode =
  | "INVALID_ARGUMENT"
  | "UNAUTHENTICATED"
  | "FORBIDDEN"
  | "NOTIFICATION_NOT_FOUND"
  | "DEPENDENCY_UNAVAILABLE"
  | "INTERNAL"
  | "NETWORK";

export class NotificationError extends Error {
  readonly status: number;
  readonly code: NotificationErrorCode;
  constructor(status: number, code: NotificationErrorCode, message: string) {
    super(message);
    this.name = "NotificationError";
    this.status = status;
    this.code = code;
  }
}

export interface Notification {
  id: string;
  errandId: string;
  // The source errand event routing key, e.g. "errand.delivered".
  type: string;
  title: string;
  body: string;
  createdAt: string;
  read: boolean;
  readAt?: string;
}

export interface NotificationPage {
  items: Notification[];
  nextCursor?: string;
}

async function parse<T>(response: Response): Promise<T> {
  const text = await response.text();
  const body = text ? JSON.parse(text) : {};
  if (!response.ok) {
    throw new NotificationError(
      response.status,
      body.code ?? "INTERNAL",
      body.message ?? `Request failed with status ${response.status}.`,
    );
  }
  return body as T;
}

function headers(token: string): Record<string, string> {
  return { Authorization: `Bearer ${token}` };
}

async function send(path: string, token: string, init?: RequestInit): Promise<Response> {
  try {
    return await fetch(`${BASE}${path}`, { ...init, headers: headers(token) });
  } catch {
    throw new NotificationError(0, "NETWORK", "Could not reach the notification service.");
  }
}

export interface ListParams {
  limit?: number;
  cursor?: string;
  unreadOnly?: boolean;
}

export async function listNotifications(token: string, params: ListParams = {}): Promise<NotificationPage> {
  const query = new URLSearchParams();
  if (params.limit) query.set("limit", String(params.limit));
  if (params.cursor) query.set("cursor", params.cursor);
  if (params.unreadOnly) query.set("unread", "true");
  const qs = query.toString();
  return parse<NotificationPage>(await send(`/v1/notifications${qs ? `?${qs}` : ""}`, token));
}

export async function getUnreadCount(token: string): Promise<number> {
  const body = await parse<{ count: number }>(await send(`/v1/notifications/unread-count`, token));
  return body.count;
}

export async function markRead(token: string, id: string): Promise<void> {
  await parse<unknown>(
    await send(`/v1/notifications/${encodeURIComponent(id)}/read`, token, { method: "POST" }),
  );
}

export async function markAllRead(token: string): Promise<number> {
  const body = await parse<{ updated: number }>(
    await send(`/v1/notifications/read-all`, token, { method: "POST" }),
  );
  return body.updated;
}

// Server-push unread badge.
//
// The service holds a Server-Sent Events connection open and pushes the unread
// count the moment one of the caller's notifications is recorded, so the badge
// is live without polling. Marking notifications read is a client action, not a
// server event, so the page that does it also announces the new count on the
// same channel (publishUnread); both the stream and that announcement feed the
// shell's badge through a single listener.

// UNREAD_EVENT carries the caller's current unread count between the
// notifications page and the shell badge as a window CustomEvent<number>.
export const UNREAD_EVENT = "foc:notifications-unread";

// CHANGED_EVENT fires when the server pushes a change over the stream — i.e. a
// new notification arrived. The shell owns the single stream and emits this so
// an open notifications list can refresh itself, not just the badge.
export const CHANGED_EVENT = "foc:notifications-changed";

// publishUnread announces a freshly known unread count to any listener (the
// shell badge) in the same tab.
export function publishUnread(count: number): void {
  window.dispatchEvent(new CustomEvent<number>(UNREAD_EVENT, { detail: count }));
}

// publishNotificationsChanged announces that the server pushed a change, so an
// open notifications page can reload its list.
export function publishNotificationsChanged(): void {
  window.dispatchEvent(new Event(CHANGED_EVENT));
}

export interface UnreadStream {
  // close ends the stream and stops reconnecting.
  close(): void;
}

// streamUnread opens the Server-Sent Events stream of the caller's unread count
// and calls onCount with each value the server pushes (one on connect, then one
// per change). The connection is read over fetch rather than EventSource so the
// JWT travels in the Authorization header and never in the URL; if it drops it
// reconnects with capped exponential backoff until close() is called.
export function streamUnread(
  getToken: () => Promise<string>,
  onCount: (count: number) => void,
): UnreadStream {
  const controller = new AbortController();
  let closed = false;
  let backoff = 1000;
  const maxBackoff = 30_000;

  const run = async () => {
    while (!closed) {
      try {
        const token = await getToken();
        const response = await fetch(`${BASE}/v1/notifications/stream`, {
          headers: { ...headers(token), Accept: "text/event-stream" },
          signal: controller.signal,
        });
        if (!response.ok || !response.body) {
          throw new Error(`stream failed with status ${response.status}`);
        }
        backoff = 1000; // a clean connect resets the backoff
        const reader = response.body.getReader();
        const decoder = new TextDecoder();
        let buffer = "";
        for (;;) {
          const { value, done } = await reader.read();
          if (done) break;
          buffer += decoder.decode(value, { stream: true });
          // SSE frames are separated by a blank line.
          let split: number;
          while ((split = buffer.indexOf("\n\n")) !== -1) {
            handleFrame(buffer.slice(0, split), onCount);
            buffer = buffer.slice(split + 2);
          }
        }
      } catch {
        if (closed) return; // abort() during close surfaces here
      }
      if (closed) return;
      await delay(backoff);
      backoff = Math.min(backoff * 2, maxBackoff);
    }
  };
  void run();

  return {
    close() {
      closed = true;
      controller.abort();
    },
  };
}

function handleFrame(frame: string, onCount: (count: number) => void): void {
  let event = "message";
  const data: string[] = [];
  for (const line of frame.split("\n")) {
    if (line.startsWith(":")) continue; // comment / heartbeat
    if (line.startsWith("event:")) event = line.slice("event:".length).trim();
    else if (line.startsWith("data:")) data.push(line.slice("data:".length).trim());
  }
  if (event !== "unread" || data.length === 0) return;
  try {
    const parsed = JSON.parse(data.join("\n")) as { count?: number };
    if (typeof parsed.count === "number") onCount(parsed.count);
  } catch {
    // Ignore a malformed frame; the next event or a reconnect recovers.
  }
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
