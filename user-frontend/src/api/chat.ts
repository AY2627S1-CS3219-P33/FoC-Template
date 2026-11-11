// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: User-frontend API client for chat service data.
// Author review: I validated correctness, edited for style, and added boundary checks.

// Chat Service client. Shapes mirror chat-service/openapi.yaml (REST) and
// chat-service/docs/websocket-protocol.md (live frames). The service is reached
// at /chat-service (its gateway route); nginx and the Vite proxy strip the
// prefix, and also forward the WebSocket upgrade on /chat-service/ws.
//
// Message bodies are plain text typed by another user: render them as text
// ({message.body} in JSX), never as HTML.

const BASE = "/chat-service";

export type ChatErrorCode =
  | "INVALID_ARGUMENT"
  | "INVALID_MESSAGE"
  | "UNAUTHENTICATED"
  | "FORBIDDEN"
  | "ROOM_NOT_FOUND"
  | "ROOM_CLOSED"
  | "RATE_LIMITED"
  | "DEPENDENCY_UNAVAILABLE"
  | "INTERNAL"
  | "NETWORK";

export class ChatError extends Error {
  readonly status: number;
  readonly code: ChatErrorCode;
  constructor(status: number, code: ChatErrorCode, message: string) {
    super(message);
    this.name = "ChatError";
    this.status = status;
    this.code = code;
  }
}

// ---- domain types ------------------------------------------------------------

export type ChatRole = "REQUESTER" | "COURIER";
// Calculated by the server when the response is made. A CLOSING room becomes
// CLOSED at closesAt without any further message from the server.
export type RoomStatus = "OPEN" | "CLOSING" | "CLOSED";
export type EndReason = "WITHDRAWN" | "COMPLETED" | "REFUNDED";

export interface ChatMessage {
  // Increases with every message; the order of a room and the key for
  // de-duplication (the same message can arrive as an ack, a live frame and a
  // REST catch-up).
  id: number;
  roomId: string;
  senderId: string;
  senderRole: ChatRole;
  body: string;
  clientMsgId: string;
  createdAt: string;
}

export interface ChatRoom {
  id: string;
  errandId: string;
  requesterId: string;
  courierId: string;
  myRole: ChatRole;
  status: RoomStatus;
  endedAt: string | null;
  endReason: EndReason | null;
  closesAt: string | null;
  createdAt: string;
  // Only on GET /me/chats.
  lastMessage?: ChatMessage;
}

export interface RoomList {
  items: ChatRoom[];
}

export interface MessageList {
  items: ChatMessage[];
}

// The service's limits (CHAT_MAX_MESSAGE_LENGTH and the clientMsgId pattern).
export const MAX_MESSAGE_LENGTH = 2000;
export const MAX_PAGE = 100;

// Counts characters as the service does (Unicode code points), so an emoji
// counts once rather than as two UTF-16 units.
export function messageLength(body: string): number {
  return [...body.trim()].length;
}

// ---- WebSocket frames --------------------------------------------------------

export interface SendFrame {
  type: "send";
  roomId: string;
  clientMsgId: string;
  body: string;
}

export type ServerFrame =
  | { type: "ready"; userId: string }
  | { type: "message"; message: ChatMessage }
  | { type: "ack"; clientMsgId: string; message: ChatMessage }
  | { type: "error"; code: ChatErrorCode; message: string; clientMsgId?: string }
  | { type: "room_opened"; room: ChatRoom }
  | {
      type: "room_closing";
      roomId: string;
      errandId: string;
      endReason: EndReason;
      closesAt: string;
    };

// chatSocketUrl is the WebSocket endpoint on the page's own origin, so it goes
// through the same gateway route as the REST calls (and wss on https pages).
export function chatSocketUrl(): string {
  const scheme = window.location.protocol === "https:" ? "wss:" : "ws:";
  return `${scheme}//${window.location.host}${BASE}/ws`;
}

// ---- HTTP plumbing -----------------------------------------------------------

async function parse<T>(response: Response): Promise<T> {
  const text = await response.text();
  let body: { code?: ChatErrorCode; message?: string } = {};
  try {
    body = text ? JSON.parse(text) : {};
  } catch {
    // A gateway error page rather than the service's JSON.
  }
  if (!response.ok) {
    throw new ChatError(
      response.status,
      body.code ?? (response.status >= 502 ? "DEPENDENCY_UNAVAILABLE" : "INTERNAL"),
      body.message ?? `Request failed with status ${response.status}.`,
    );
  }
  return body as T;
}

async function request<T>(path: string, init: RequestInit): Promise<T> {
  let response: Response;
  try {
    response = await fetch(`${BASE}${path}`, init);
  } catch {
    throw new ChatError(0, "NETWORK", "Could not reach the chat service.");
  }
  return parse<T>(response);
}

function headers(token: string, json = false): Record<string, string> {
  const h: Record<string, string> = { Authorization: `Bearer ${token}` };
  if (json) h["Content-Type"] = "application/json";
  return h;
}

// ---- rooms -------------------------------------------------------------------

// getMyChats lists every room the caller takes part in, newest first, each with
// its newest message.
export function getMyChats(token: string): Promise<RoomList> {
  return request<RoomList>("/me/chats", { headers: headers(token) });
}

// getChatsForErrand lists the caller's rooms for one errand, newest first. A
// courier has at most one; a requester has one per courier who accepted. It is
// empty for a moment after the errand is accepted, until the service has
// processed the accept event.
export function getChatsForErrand(token: string, errandId: string): Promise<RoomList> {
  return request<RoomList>(`/errands/${encodeURIComponent(errandId)}/chats`, {
    headers: headers(token),
  });
}

// ---- messages ----------------------------------------------------------------

export interface MessagePageParams {
  // Messages newer than this ID (catch-up after a reconnect).
  after?: number;
  // Messages older than this ID (scrolling back).
  before?: number;
  // 1 to 100; the service defaults to 50.
  limit?: number;
}

// getMessages returns one page, always oldest first. With neither after nor
// before it returns the newest page.
export function getMessages(
  token: string,
  roomId: string,
  params: MessagePageParams = {},
): Promise<MessageList> {
  const search = new URLSearchParams();
  if (params.after !== undefined) search.set("after", String(params.after));
  if (params.before !== undefined) search.set("before", String(params.before));
  if (params.limit !== undefined) search.set("limit", String(params.limit));
  const qs = search.toString();
  return request<MessageList>(
    `/chats/${encodeURIComponent(roomId)}/messages${qs ? `?${qs}` : ""}`,
    { headers: headers(token) },
  );
}

// postMessage sends without the WebSocket (the fallback while it is
// disconnected). Reusing clientMsgId on a retry returns the stored message
// instead of posting it twice.
export function postMessage(
  token: string,
  roomId: string,
  clientMsgId: string,
  body: string,
): Promise<ChatMessage> {
  return request<ChatMessage>(`/chats/${encodeURIComponent(roomId)}/messages`, {
    method: "POST",
    headers: headers(token, true),
    body: JSON.stringify({ clientMsgId, body }),
  });
}

// chatErrorText turns a stable error code into something a user can act on.
export function chatErrorText(code: ChatErrorCode | string, fallback?: string): string {
  switch (code) {
    case "NETWORK":
      return "Could not reach the chat service.";
    case "UNAUTHENTICATED":
      return "Please sign in again to continue.";
    case "FORBIDDEN":
      return "Your account is not allowed to use chat.";
    case "ROOM_NOT_FOUND":
      return "This chat no longer exists, or you are not part of it.";
    case "ROOM_CLOSED":
      return "This chat is closed; messages can no longer be sent.";
    case "RATE_LIMITED":
      return "You are sending too quickly. Wait a moment, then retry.";
    case "INVALID_MESSAGE":
      return fallback ?? "That message cannot be sent.";
    case "DEPENDENCY_UNAVAILABLE":
      return "The chat service is temporarily unavailable. Please try again shortly.";
    default:
      return fallback ?? "Something went wrong. Please try again.";
  }
}

// describeChatError explains anything a chat call can throw. Every failure
// from the service itself is a ChatError; anything else came from getting the
// Auth0 token for the chat API.
export function describeChatError(err: unknown): string {
  if (err instanceof ChatError) return chatErrorText(err.code, err.message);
  return "Could not get access to chat. Try signing out and in again.";
}
