// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: User-frontend chat interface and useChatSocket behavior.
// Author review: I validated correctness, edited for style, and added boundary checks.

import { useCallback, useEffect, useRef, useState } from "react";
import { chatSocketUrl, type SendFrame, type ServerFrame } from "../api/chat";

// The live chat connection (chat-service/docs/websocket-protocol.md).
//
// One socket per signed-in tab carries every room the user is in: the server
// routes frames by user, so nothing joins or leaves rooms. The hook connects,
// sends the auth frame (browsers cannot set an Authorization header on a
// WebSocket, and a token in the URL would be logged), and reconnects with
// backoff. Messages missed while disconnected are never replayed over the
// socket, so every "ready" frame is passed on and the caller catches up over
// REST.

export type ChatSocketStatus =
  // Not wanted (no chat on this screen).
  | "idle"
  // First connection in progress.
  | "connecting"
  // Authenticated; frames flow both ways.
  | "ready"
  // Lost the connection and waiting to try again.
  | "reconnecting"
  // The account cannot use chat (close code 4403). Never retried.
  | "forbidden";

// 1 s, 2 s, 5 s, then up to 30 s, each with ±20 % randomness so many browsers
// do not all reconnect at the same moment after a deploy or an outage.
const BACKOFF_MS = [1000, 2000, 5000, 10000, 20000, 30000];

function jitter(ms: number): number {
  return Math.round(ms * (0.8 + Math.random() * 0.4));
}

interface Options {
  // Connect only while true; turning it off closes the socket.
  enabled: boolean;
  // A token for the chat-service audience with chats:use. Called on every
  // (re)connect, so an expired token (close code 4401) is replaced by a fresh
  // one from the auth cache.
  getToken: () => Promise<string>;
  // Every frame from the server, including "ready".
  onFrame: (frame: ServerFrame) => void;
}

export interface ChatSocket {
  status: ChatSocketStatus;
  // Sends a frame if the socket is ready; false means it was not sent and the
  // caller should use the REST fallback.
  send: (frame: SendFrame) => boolean;
}

export function useChatSocket({ enabled, getToken, onFrame }: Options): ChatSocket {
  const [status, setStatus] = useState<ChatSocketStatus>("idle");
  const socketRef = useRef<WebSocket | null>(null);
  const readyRef = useRef(false);

  // The latest callbacks, so a new function identity never reconnects.
  const getTokenRef = useRef(getToken);
  const onFrameRef = useRef(onFrame);
  useEffect(() => {
    getTokenRef.current = getToken;
    onFrameRef.current = onFrame;
  });

  useEffect(() => {
    if (!enabled) {
      setStatus("idle");
      return;
    }

    let disposed = false;
    let attempt = 0;
    let timer: number | undefined;
    let socket: WebSocket | null = null;

    function retryIn(delay: number) {
      setStatus("reconnecting");
      timer = window.setTimeout(() => {
        timer = undefined;
        void connect();
      }, delay);
    }

    function nextBackoff(): number {
      const delay = BACKOFF_MS[Math.min(attempt, BACKOFF_MS.length - 1)];
      attempt += 1;
      return jitter(delay);
    }

    async function connect() {
      if (disposed) return;
      setStatus((current) => (current === "reconnecting" ? current : "connecting"));

      let token: string;
      try {
        token = await getTokenRef.current();
      } catch {
        // Auth0 unreachable or the session needs a fresh sign-in; keep trying
        // quietly, as the rest of the screen still works.
        if (!disposed) retryIn(nextBackoff());
        return;
      }
      if (disposed) return;

      const ws = new WebSocket(chatSocketUrl());
      socket = ws;
      socketRef.current = ws;

      ws.onopen = () => {
        ws.send(JSON.stringify({ type: "auth", token }));
      };

      ws.onmessage = (event) => {
        if (typeof event.data !== "string") return;
        let frame: ServerFrame;
        try {
          frame = JSON.parse(event.data) as ServerFrame;
        } catch {
          return;
        }
        if (frame.type === "ready") {
          attempt = 0;
          readyRef.current = true;
          setStatus("ready");
        }
        onFrameRef.current(frame);
      };

      ws.onclose = (event) => {
        if (socketRef.current === ws) {
          socketRef.current = null;
          readyRef.current = false;
        }
        if (disposed) return;
        switch (event.code) {
          case 4403:
            // Valid token without chats:use, or not a user token. Retrying
            // cannot help.
            setStatus("forbidden");
            return;
          case 1012:
            // The service is restarting (a deploy): come straight back.
            retryIn(Math.round(Math.random() * 1000));
            return;
          case 4429:
            // Too many tabs open for this user; wait the longest step.
            attempt = BACKOFF_MS.length - 1;
            retryIn(nextBackoff());
            return;
          default:
            // 1006 lost, 1008 too slow or bad frame, 1013 Auth0 down, 4401
            // token rejected or expired (getToken renews it), and anything else.
            retryIn(nextBackoff());
        }
      };
    }

    // Coming back online skips the rest of the wait.
    function onOnline() {
      if (timer === undefined) return;
      window.clearTimeout(timer);
      timer = undefined;
      void connect();
    }

    window.addEventListener("online", onOnline);
    void connect();

    return () => {
      disposed = true;
      window.removeEventListener("online", onOnline);
      if (timer !== undefined) window.clearTimeout(timer);
      if (socket) socket.close(1000);
      socketRef.current = null;
      readyRef.current = false;
    };
  }, [enabled]);

  const send = useCallback((frame: SendFrame) => {
    const ws = socketRef.current;
    if (!ws || !readyRef.current || ws.readyState !== WebSocket.OPEN) return false;
    ws.send(JSON.stringify(frame));
    return true;
  }, []);

  return { status, send };
}
