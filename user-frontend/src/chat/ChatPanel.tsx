// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: User-frontend chat interface and ChatPanel behavior.
// Author review: I validated correctness, edited for style, and added boundary checks.

import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type KeyboardEvent,
} from "react";
import {
  ChatError,
  chatErrorText,
  describeChatError as describe,
  getMessages,
  MAX_MESSAGE_LENGTH,
  MAX_PAGE,
  messageLength,
  postMessage,
  type ChatMessage,
  type ChatRole,
  type ChatRoom,
  type SendFrame,
  type ServerFrame,
} from "../api/chat";
import type { ChatSocketStatus } from "./useChatSocket";
import { endedText, useRoomStatus } from "./roomStatus";
import { formatDateTime, timeLeft } from "../pages/errands/format";

// One room's conversation: history, live messages and the message box.
//
// Messages can reach this panel three ways (the ack to our own send, the live
// "message" frame, and a REST catch-up), so they are merged by id and kept in
// id order. A message being sent is shown at once as "sending" and replaced by
// the stored one when it comes back with the same clientMsgId; a send that
// fails or is not confirmed can be retried with the same clientMsgId, so the
// server never stores it twice.
//
// When the errand ends for the room it is CLOSING: a banner says when it
// closes, and sending still works. At closesAt it becomes CLOSED and
// ErrandChat stops showing it (the conversation stays in chat-service for
// admins). A ROOM_CLOSED answer from the server (the browser's clock was
// behind) is reported with onClosed, which hides it at once.

interface Props {
  room: ChatRoom;
  getToken: () => Promise<string>;
  socketStatus: ChatSocketStatus;
  send: (frame: SendFrame) => boolean;
  // Registers a listener for server frames; returns the unsubscribe function.
  subscribe: (listener: (frame: ServerFrame) => void) => () => void;
  // The server refused a send with ROOM_CLOSED, so the room is closed
  // whatever this browser's clock says.
  forcedClosed: boolean;
  onClosed: (roomId: string) => void;
}

interface Pending {
  clientMsgId: string;
  body: string;
  state: "sending" | "failed";
  error?: string;
}

const FIRST_PAGE = 50;
// A send not acknowledged by then is offered for retry.
const ACK_TIMEOUT_MS = 10_000;
// While the socket is down, other people's messages are fetched this often.
const OFFLINE_POLL_MS = 15_000;
// The counter appears once a message is this close to the limit.
const COUNTER_FROM = MAX_MESSAGE_LENGTH - 200;

const ROLE_LABEL: Record<ChatRole, string> = {
  REQUESTER: "Requester",
  COURIER: "Courier",
};

export function ChatPanel({
  room,
  getToken,
  socketStatus,
  send,
  subscribe,
  forcedClosed,
  onClosed,
}: Props) {
  const { status, now } = useRoomStatus(room, forcedClosed);
  const closed = status === "CLOSED";
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [pending, setPending] = useState<Pending[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [hasEarlier, setHasEarlier] = useState(false);
  const [loadingEarlier, setLoadingEarlier] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [draft, setDraft] = useState("");

  // The source of truth for merging; the state above mirrors it for rendering.
  const messagesRef = useRef<ChatMessage[]>([]);
  const timers = useRef(new Map<string, number>());

  // Follow new messages only while the reader is at the bottom, and keep the
  // reader's place when older messages are added above.
  const logRef = useRef<HTMLDivElement>(null);
  const stickToBottom = useRef(true);
  const keepFromBottom = useRef<number | null>(null);

  const clearTimer = useCallback((clientMsgId: string) => {
    const handle = timers.current.get(clientMsgId);
    if (handle !== undefined) {
      window.clearTimeout(handle);
      timers.current.delete(clientMsgId);
    }
  }, []);

  useEffect(() => {
    const all = timers.current;
    return () => {
      all.forEach((handle) => window.clearTimeout(handle));
      all.clear();
    };
  }, []);

  const fail = useCallback(
    (clientMsgId: string, error: string) => {
      clearTimer(clientMsgId);
      setPending((current) =>
        current.map((p) =>
          p.clientMsgId === clientMsgId ? { ...p, state: "failed", error } : p,
        ),
      );
    },
    [clearTimer],
  );

  const merge = useCallback(
    (incoming: ChatMessage[]) => {
      const fresh = incoming.filter((m) => m.roomId === room.id);
      if (fresh.length === 0) return;

      const byId = new Map(messagesRef.current.map((m) => [m.id, m]));
      let changed = false;
      for (const m of fresh) {
        if (!byId.has(m.id)) {
          byId.set(m.id, m);
          changed = true;
        }
      }
      if (changed) {
        const next = [...byId.values()].sort((a, b) => a.id - b.id);
        messagesRef.current = next;
        setMessages(next);
      }

      // Our own messages settle the matching "sending" bubble.
      const settled = new Set(
        fresh.filter((m) => m.senderRole === room.myRole).map((m) => m.clientMsgId),
      );
      if (settled.size > 0) {
        settled.forEach(clearTimer);
        setPending((current) => current.filter((p) => !settled.has(p.clientMsgId)));
      }
    },
    [room.id, room.myRole, clearTimer],
  );

  // ---- loading -----------------------------------------------------------------

  // catchUp loads the newest page the first time, and afterwards everything
  // newer than the last message held. Calls made while one is running are
  // folded into one more pass.
  const syncing = useRef(false);
  const syncAgain = useRef(false);

  const catchUp = useCallback(async () => {
    if (syncing.current) {
      syncAgain.current = true;
      return;
    }
    syncing.current = true;
    try {
      do {
        syncAgain.current = false;
        try {
          const token = await getToken();
          const held = messagesRef.current;
          if (held.length === 0) {
            const page = await getMessages(token, room.id, { limit: FIRST_PAGE });
            merge(page.items);
            setHasEarlier(page.items.length === FIRST_PAGE);
          } else {
            let after = held[held.length - 1].id;
            // Bounded, in case the room is very busy.
            for (let i = 0; i < 20; i++) {
              const page = await getMessages(token, room.id, { after, limit: MAX_PAGE });
              merge(page.items);
              if (page.items.length < MAX_PAGE) break;
              after = page.items[page.items.length - 1].id;
            }
          }
          setLoadError(null);
        } catch (err) {
          setLoadError(describe(err));
        }
      } while (syncAgain.current);
    } finally {
      syncing.current = false;
      setLoaded(true);
    }
  }, [getToken, room.id, merge]);

  useEffect(() => {
    void catchUp();
  }, [catchUp]);

  // Without the socket nothing is pushed, so poll until it is back.
  const live = socketStatus === "ready";
  useEffect(() => {
    if (closed || live || socketStatus === "idle" || socketStatus === "forbidden") return;
    const handle = window.setInterval(() => void catchUp(), OFFLINE_POLL_MS);
    return () => window.clearInterval(handle);
  }, [closed, live, socketStatus, catchUp]);

  // ---- live frames -------------------------------------------------------------

  useEffect(
    () =>
      subscribe((frame) => {
        switch (frame.type) {
          case "ready":
            // Anything sent while we were disconnected is only in the database.
            void catchUp();
            break;
          case "message":
          case "ack":
            merge([frame.message]);
            break;
          case "error":
            if (!frame.clientMsgId) break;
            fail(frame.clientMsgId, chatErrorText(frame.code, frame.message));
            if (frame.code === "ROOM_CLOSED") onClosed(room.id);
            break;
        }
      }),
    [subscribe, catchUp, merge, fail, onClosed, room.id],
  );

  // ---- sending -----------------------------------------------------------------

  const deliver = useCallback(
    async (clientMsgId: string, body: string) => {
      setPending((current) =>
        current.map((p) =>
          p.clientMsgId === clientMsgId ? { clientMsgId, body, state: "sending" } : p,
        ),
      );
      clearTimer(clientMsgId);
      timers.current.set(
        clientMsgId,
        window.setTimeout(
          () => fail(clientMsgId, "Not confirmed. Check your connection, then retry."),
          ACK_TIMEOUT_MS,
        ),
      );

      if (send({ type: "send", roomId: room.id, clientMsgId, body })) return;

      // No live connection: the REST route has the same rules, stores the
      // message once per clientMsgId and delivers it live to the other person.
      try {
        const token = await getToken();
        merge([await postMessage(token, room.id, clientMsgId, body)]);
      } catch (err) {
        fail(clientMsgId, describe(err));
        if (err instanceof ChatError && err.code === "ROOM_CLOSED") onClosed(room.id);
      }
    },
    [send, room.id, getToken, merge, fail, clearTimer, onClosed],
  );

  const length = messageLength(draft);
  const tooLong = length > MAX_MESSAGE_LENGTH;

  function submit() {
    const body = draft.trim();
    if (body.length === 0 || tooLong || closed) return;
    const clientMsgId = crypto.randomUUID();
    setPending((current) => [...current, { clientMsgId, body, state: "sending" }]);
    setDraft("");
    stickToBottom.current = true;
    void deliver(clientMsgId, body);
  }

  function onKeyDown(event: KeyboardEvent<HTMLTextAreaElement>) {
    // Enter sends; Shift+Enter is a new line. Never interrupt an IME.
    if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) {
      event.preventDefault();
      submit();
    }
  }

  async function loadEarlier() {
    const first = messagesRef.current[0];
    if (!first) return;
    setLoadingEarlier(true);
    try {
      const token = await getToken();
      const page = await getMessages(token, room.id, { before: first.id, limit: FIRST_PAGE });
      const log = logRef.current;
      if (log) keepFromBottom.current = log.scrollHeight - log.scrollTop;
      merge(page.items);
      setHasEarlier(page.items.length === FIRST_PAGE);
      setLoadError(null);
    } catch (err) {
      setLoadError(describe(err));
    } finally {
      setLoadingEarlier(false);
    }
  }

  // ---- scrolling ---------------------------------------------------------------

  useLayoutEffect(() => {
    const log = logRef.current;
    if (!log) return;
    if (keepFromBottom.current !== null) {
      log.scrollTop = log.scrollHeight - keepFromBottom.current;
      keepFromBottom.current = null;
    } else if (stickToBottom.current) {
      log.scrollTop = log.scrollHeight;
    }
  }, [messages, pending, loaded]);

  function onScroll() {
    const log = logRef.current;
    if (log) stickToBottom.current = log.scrollHeight - log.scrollTop - log.clientHeight < 48;
  }

  // ---- render ------------------------------------------------------------------

  let previousDay = "";

  return (
    <div className="chat">
      {status === "CLOSING" && room.closesAt && (
        <div className="banner banner-wait chat-banner" role="status">
          <span>
            {endedText(room.endReason, room.myRole)} You can keep chatting until{" "}
            {formatDateTime(room.closesAt)} ({timeLeft(room.closesAt, now)}); after that this chat
            closes and is no longer shown here.
          </span>
        </div>
      )}

      {loadError && (
        <div className="banner banner-error chat-banner" role="alert">
          <span>{loadError}</span>
          <button type="button" className="link-btn" onClick={() => void catchUp()}>
            Retry
          </button>
        </div>
      )}

      <div
        className="chat-log"
        ref={logRef}
        onScroll={onScroll}
        role="log"
        aria-live="polite"
        aria-label="Messages"
      >
        {hasEarlier && (
          <button
            type="button"
            className="link-btn chat-earlier"
            onClick={() => void loadEarlier()}
            disabled={loadingEarlier}
          >
            {loadingEarlier ? "Loading…" : "Show earlier messages"}
          </button>
        )}

        {!loaded && <p className="muted small center">Loading messages…</p>}
        {loaded && messages.length === 0 && pending.length === 0 && (
          <p className="muted small center">No messages yet. Say hello, or agree where to meet.</p>
        )}

        <ol className="chat-list">
          {messages.map((m) => {
            const day = dayLabel(m.createdAt);
            const divider = day !== previousDay;
            previousDay = day;
            const mine = m.senderRole === room.myRole;
            return (
              <li key={m.id} className="chat-item">
                {divider && <div className="chat-day">{day}</div>}
                <div className={`chat-msg ${mine ? "mine" : "theirs"}`}>
                  {/* Plain text only: never render a message as HTML. */}
                  <p className="chat-body">{m.body}</p>
                  <span className="chat-meta">
                    {mine ? "You" : ROLE_LABEL[m.senderRole]} · {timeLabel(m.createdAt)}
                  </span>
                </div>
              </li>
            );
          })}
          {pending.map((p) => (
            <li key={p.clientMsgId} className="chat-item">
              <div className={`chat-msg mine ${p.state}`}>
                <p className="chat-body">{p.body}</p>
                {p.state === "sending" ? (
                  <span className="chat-meta">Sending…</span>
                ) : (
                  <span className="chat-meta chat-failed">
                    {p.error ?? "Not sent."}{" "}
                    {!closed && (
                      <>
                        <button
                          type="button"
                          className="link-btn"
                          onClick={() => void deliver(p.clientMsgId, p.body)}
                        >
                          Retry
                        </button>
                        {" · "}
                      </>
                    )}
                    <button
                      type="button"
                      className="link-btn"
                      onClick={() => {
                        clearTimer(p.clientMsgId);
                        setPending((current) =>
                          current.filter((x) => x.clientMsgId !== p.clientMsgId),
                        );
                      }}
                    >
                      Discard
                    </button>
                  </span>
                )}
              </div>
            </li>
          ))}
        </ol>
      </div>

      {/* ErrandChat hides a CLOSED room, so this only covers the moment
          between the room closing and the parent re-rendering. */}
      {!closed && (
        <>
          <form
            className="chat-compose"
            onSubmit={(event) => {
              event.preventDefault();
              submit();
            }}
          >
            <label htmlFor={`chat-input-${room.id}`} className="sr-only">
              Message
            </label>
            <textarea
              id={`chat-input-${room.id}`}
              className="input chat-input"
              rows={2}
              placeholder="Write a message"
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              onKeyDown={onKeyDown}
              aria-invalid={tooLong}
              aria-describedby={`chat-hint-${room.id}`}
            />
            <button type="submit" className="btn slim" disabled={length === 0 || tooLong}>
              Send
            </button>
          </form>
          <p id={`chat-hint-${room.id}`} className="chat-hint muted small">
            <span>Enter to send · Shift+Enter for a new line</span>
            {length >= COUNTER_FROM && (
              <span className={tooLong ? "chat-counter over" : "chat-counter"}>
                {length}/{MAX_MESSAGE_LENGTH}
              </span>
            )}
          </p>
        </>
      )}
    </div>
  );
}

function timeLabel(iso: string): string {
  const at = new Date(iso);
  if (!Number.isFinite(at.getTime())) return "";
  return at.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}

function dayLabel(iso: string): string {
  const at = new Date(iso);
  if (!Number.isFinite(at.getTime())) return "";
  const today = new Date();
  const yesterday = new Date();
  yesterday.setDate(today.getDate() - 1);
  if (at.toDateString() === today.toDateString()) return "Today";
  if (at.toDateString() === yesterday.toDateString()) return "Yesterday";
  return at.toLocaleDateString(undefined, { weekday: "short", day: "numeric", month: "short" });
}
