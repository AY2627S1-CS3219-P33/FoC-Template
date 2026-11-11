// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: User-frontend chat interface and roomStatus behavior.
// Author review: I validated correctness, edited for style, and added boundary checks.

import { useEffect, useState } from "react";
import type { ChatRole, ChatRoom, EndReason, RoomStatus } from "../api/chat";

// A room's state, worked out in the browser from closesAt, as the server
// does. The server sends no "room closed" frame: the browser switches a
// CLOSING room to CLOSED itself when closesAt passes. If the browser's clock
// is behind, the server's ROOM_CLOSED answer to the next send corrects it
// (forcedClosed).
export function roomStatus(room: ChatRoom, now: number, forcedClosed = false): RoomStatus {
  if (forcedClosed) return "CLOSED";
  if (!room.closesAt) return "OPEN";
  const closesAt = Date.parse(room.closesAt);
  if (!Number.isFinite(closesAt)) return room.status;
  return now < closesAt ? "CLOSING" : "CLOSED";
}

// setTimeout cannot wait longer than this (about 24.8 days).
export const MAX_TIMEOUT_MS = 2 ** 31 - 1;
// How often the "closes in …" text is refreshed.
const TICK_MS = 30_000;

// useRoomStatus re-renders when a CLOSING room reaches closesAt, and every
// half minute before that so the remaining time stays current.
export function useRoomStatus(
  room: ChatRoom,
  forcedClosed: boolean,
): { status: RoomStatus; now: number } {
  const [now, setNow] = useState(() => Date.now());
  const status = roomStatus(room, now, forcedClosed);

  useEffect(() => {
    if (status !== "CLOSING" || !room.closesAt) return;
    const closesAt = Date.parse(room.closesAt);
    const tick = window.setInterval(() => setNow(Date.now()), TICK_MS);
    const close = window.setTimeout(
      () => setNow(Date.now()),
      Math.min(Math.max(closesAt - Date.now() + 50, 0), MAX_TIMEOUT_MS),
    );
    return () => {
      window.clearInterval(tick);
      window.clearTimeout(close);
    };
  }, [status, room.closesAt]);

  // A new closing time (a room_closing frame) is measured from the real now.
  useEffect(() => {
    setNow(Date.now());
  }, [room.closesAt]);

  return { status, now };
}

// endedText says why the errand ended, from the reader's side.
export function endedText(reason: EndReason | null, myRole: ChatRole): string {
  switch (reason) {
    case "WITHDRAWN":
      return myRole === "COURIER"
        ? "You withdrew from this errand."
        : "This courier withdrew from the errand.";
    case "COMPLETED":
      return "This errand is complete.";
    case "REFUNDED":
      return "This errand was refunded.";
    default:
      return "This errand has ended.";
  }
}
