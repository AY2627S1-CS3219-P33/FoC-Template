import { useCallback, useEffect, useRef, useState } from "react";
import {
  describeChatError,
  getChatsForErrand,
  type ChatRoom,
  type ServerFrame,
} from "../api/chat";
import type { ErrandStatus } from "../api/order";
import { formatDateTime } from "../pages/errands/format";
import { ChatPanel } from "./ChatPanel";
import { useChatSocket, type ChatSocketStatus } from "./useChatSocket";

// The chat section of an errand, for its requester or courier.
//
// It owns the errand's room list and the one live connection, and shows one
// room at a time. A courier has at most one room. A requester has one per
// courier who accepted: the newest (the current courier's) is shown first and
// earlier couriers' rooms can be picked from a list.
//
// A room is created by chat-service when it processes the accept event, which
// is usually under a second after accepting but not instantly. So while the
// errand has a courier and no room has appeared yet, this shows "Connecting
// chat…" and picks the room up from the room_opened frame, a re-read after
// each (re)connect, and a short poll.

interface Props {
  errandId: string;
  errandStatus: ErrandStatus;
  // A token for the chat-service audience with chats:use.
  getToken: () => Promise<string>;
}

// The statuses in which the errand has a courier, so a room should exist.
const HAS_COURIER: readonly ErrandStatus[] = [
  "ASSIGNED",
  "PICKED_UP",
  "DELIVERED",
  "REVIEW_REQUIRED",
];

const WAIT_POLL_MS = 2_000;
const WAIT_LIMIT_MS = 15_000;

export function ErrandChat({ errandId, errandStatus, getToken }: Props) {
  const [rooms, setRooms] = useState<ChatRoom[] | null>(null);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [slow, setSlow] = useState(false);
  // Bumped by "Try again" to restart the wait.
  const [waitRound, setWaitRound] = useState(0);

  const loadRooms = useCallback(async () => {
    try {
      const token = await getToken();
      const list = await getChatsForErrand(token, errandId);
      setRooms(list.items);
      setError(null);
    } catch (err) {
      setError(describeChatError(err));
      setRooms((current) => current ?? []);
    }
  }, [errandId, getToken]);

  useEffect(() => {
    void loadRooms();
  }, [loadRooms]);

  const hasCourier = HAS_COURIER.includes(errandStatus);
  const hasRooms = rooms !== null && rooms.length > 0;
  const waiting = rooms !== null && !hasRooms && hasCourier;

  // Frames go to the open panel through these listeners, so switching rooms
  // keeps the same connection.
  const listeners = useRef(new Set<(frame: ServerFrame) => void>());
  const subscribe = useCallback((listener: (frame: ServerFrame) => void) => {
    listeners.current.add(listener);
    return () => {
      listeners.current.delete(listener);
    };
  }, []);

  const onFrame = useCallback(
    (frame: ServerFrame) => {
      if (frame.type === "ready") {
        // A room opened while we were disconnected is only in the database.
        void loadRooms();
      } else if (frame.type === "room_opened" && frame.room.errandId === errandId) {
        const room = frame.room;
        setRooms((current) => {
          const list = current ?? [];
          return list.some((r) => r.id === room.id) ? list : [room, ...list];
        });
        // A new courier's room is the one that matters now.
        setSelectedId(room.id);
      }
      listeners.current.forEach((listener) => listener(frame));
    },
    [errandId, loadRooms],
  );

  const { status, send } = useChatSocket({
    enabled: hasRooms || hasCourier,
    getToken,
    onFrame,
  });

  useEffect(() => {
    setSlow(false);
    if (!waiting) return;
    const poll = window.setInterval(() => void loadRooms(), WAIT_POLL_MS);
    const limit = window.setTimeout(() => {
      window.clearInterval(poll);
      setSlow(true);
    }, WAIT_LIMIT_MS);
    return () => {
      window.clearInterval(poll);
      window.clearTimeout(limit);
    };
  }, [waiting, waitRound, loadRooms]);

  // Nothing to show: no courier yet, and never had one.
  if (rooms === null || (!hasRooms && !hasCourier)) return null;

  if (!hasRooms) {
    return (
      <section className="panel chat-panel">
        <div className="chat-head">
          <h2>Chat</h2>
        </div>
        {error ? (
          <div className="banner banner-error" role="alert">
            <span>{error}</span>
          </div>
        ) : slow ? (
          <p className="muted">
            Chat has not started for this errand yet. If it was accepted just now, try again in a
            moment.
          </p>
        ) : (
          <p className="muted">Connecting chat…</p>
        )}
        {(error || slow) && (
          <button
            type="button"
            className="btn outline slim"
            onClick={() => {
              setError(null);
              setWaitRound((n) => n + 1);
              void loadRooms();
            }}
          >
            Try again
          </button>
        )}
      </section>
    );
  }

  const room = rooms.find((r) => r.id === selectedId) ?? rooms[0];
  const otherParty = room.myRole === "REQUESTER" ? "your courier" : "the requester";

  return (
    <section className="panel chat-panel">
      <div className="chat-head">
        <h2>Chat with {otherParty}</h2>
        <ConnectionLabel status={status} />
      </div>

      {rooms.length > 1 && (
        <div className="field chat-rooms">
          <label htmlFor={`chat-room-${errandId}`}>Conversation</label>
          <select
            id={`chat-room-${errandId}`}
            className="input"
            value={room.id}
            onChange={(e) => setSelectedId(e.target.value)}
          >
            {rooms.map((r, index) => (
              <option key={r.id} value={r.id}>
                {index === 0 ? "Latest courier" : "Earlier courier"} · since{" "}
                {formatDateTime(r.createdAt)}
              </option>
            ))}
          </select>
        </div>
      )}

      {error && (
        <div className="banner banner-error" role="alert">
          <span>{error}</span>
        </div>
      )}

      <ChatPanel
        key={room.id}
        room={room}
        getToken={getToken}
        socketStatus={status}
        send={send}
        subscribe={subscribe}
      />
    </section>
  );
}

function ConnectionLabel({ status }: { status: ChatSocketStatus }) {
  switch (status) {
    case "ready":
      return <span className="chat-conn live">Live</span>;
    case "connecting":
      return <span className="chat-conn">Connecting…</span>;
    case "reconnecting":
      return (
        <span className="chat-conn" title="Messages still send, and new ones are checked regularly.">
          Reconnecting…
        </span>
      );
    case "forbidden":
      return <span className="chat-conn">Live updates unavailable</span>;
    default:
      return null;
  }
}
