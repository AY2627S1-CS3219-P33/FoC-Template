// Notification Service client.
//
// Mirrors notification-service/openapi.yaml (v1.0.0). Every route is scoped to
// the caller by their token: there is no way to read or change another user's
// notifications, and notifications are created only by the service from errand
// events, never over HTTP. The list is keyset-paginated newest-first; pass the
// nextCursor from one page to fetch the next.

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
