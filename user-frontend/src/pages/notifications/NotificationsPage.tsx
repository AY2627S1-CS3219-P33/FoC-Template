// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: User-frontend notifications page and NotificationsPage behavior.
// Author review: I validated correctness, edited for style, and added boundary checks.

import { useCallback, useEffect, useState } from "react";
import { useAuth } from "../../auth/AuthContext";
import {
  CHANGED_EVENT,
  NotificationError,
  getUnreadCount,
  listNotifications,
  markAllRead,
  markRead,
  publishUnread,
  type Notification,
} from "../../api/notification";

const NOTIFICATION_AUDIENCE =
  import.meta.env.VITE_NOTIFICATION_AUDIENCE || "https://api.foc.local/notification-service";

const PAGE_SIZE = 20;

function notificationError(err: unknown): string {
  if (err instanceof NotificationError) {
    if (err.code === "UNAUTHENTICATED") return "Please sign in again to see your notifications.";
    if (err.code === "DEPENDENCY_UNAVAILABLE" || err.code === "NETWORK")
      return "The notification service is temporarily unavailable.";
    return err.message;
  }
  return "Could not reach the notification service.";
}

// relativeTime renders a short "2h ago" style label, falling back to a date for
// anything older than a week.
function relativeTime(iso: string): string {
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return "";
  const seconds = Math.round((Date.now() - then) / 1000);
  if (seconds < 60) return "just now";
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.round(hours / 24);
  if (days < 7) return `${days}d ago`;
  return new Date(iso).toLocaleDateString();
}

export function NotificationsPage() {
  const { getAccessToken } = useAuth();

  const getToken = useCallback(
    () =>
      getAccessToken({
        audience: NOTIFICATION_AUDIENCE,
        scope: "openid profile email notifications:read:own notifications:write:own",
      }),
    [getAccessToken],
  );

  const [items, setItems] = useState<Notification[]>([]);
  const [cursor, setCursor] = useState<string | undefined>();
  const [unread, setUnread] = useState(0);
  const [unreadOnly, setUnreadOnly] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);

  // load fetches the first page for the current filter; loadMore appends. A
  // silent load refreshes the first page in the background (e.g. when the server
  // pushes a new notification) without the loading flash or clearing the list on
  // a transient error, so the list updates live under the user.
  const load = useCallback(
    async (append: boolean, silent = false) => {
      if (!silent) {
        if (append) setBusy(true);
        else setLoading(true);
      }
      try {
        const token = await getToken();
        const [page, count] = await Promise.all([
          listNotifications(token, {
            limit: PAGE_SIZE,
            unreadOnly,
            cursor: append ? cursor : undefined,
          }),
          getUnreadCount(token),
        ]);
        setItems((prev) => (append ? [...prev, ...page.items] : page.items));
        setCursor(page.nextCursor);
        setUnread(count);
        publishUnread(count);
        setError(null);
      } catch (err) {
        if (!silent) setError(notificationError(err));
      } finally {
        if (!silent) {
          setLoading(false);
          setBusy(false);
        }
      }
    },
    [getToken, unreadOnly, cursor],
  );

  // When the shell's stream reports a new notification, refresh the list in
  // place so it updates without the user having to switch tabs or click around.
  useEffect(() => {
    const onChanged = () => void load(false, true);
    window.addEventListener(CHANGED_EVENT, onChanged);
    return () => window.removeEventListener(CHANGED_EVENT, onChanged);
  }, [load]);

  // Reload from the top whenever the filter changes.
  useEffect(() => {
    void load(false);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [unreadOnly]);

  async function onMarkRead(n: Notification) {
    if (n.read) return;
    try {
      const token = await getToken();
      await markRead(token, n.id);
      setItems((prev) =>
        unreadOnly
          ? prev.filter((x) => x.id !== n.id)
          : prev.map((x) => (x.id === n.id ? { ...x, read: true } : x)),
      );
      setUnread((c) => {
        const next = Math.max(0, c - 1);
        publishUnread(next);
        return next;
      });
    } catch (err) {
      setError(notificationError(err));
    }
  }

  async function onMarkAll() {
    try {
      const token = await getToken();
      await markAllRead(token);
      setUnread(0);
      publishUnread(0);
      if (unreadOnly) setItems([]);
      else setItems((prev) => prev.map((x) => ({ ...x, read: true })));
    } catch (err) {
      setError(notificationError(err));
    }
  }

  return (
    <>
      <div className="page-head">
        <div>
          <h1>Notifications</h1>
          <p className="muted" style={{ margin: "0.3rem 0 0" }}>
            Updates about your errands — when one is accepted, picked up, delivered, completed or needs
            your attention.
          </p>
        </div>
        <button
          type="button"
          className="btn outline slim"
          onClick={onMarkAll}
          disabled={loading || unread === 0}
        >
          Mark all read
        </button>
      </div>

      <div className="tabs" role="tablist" style={{ marginBottom: "1rem" }}>
        <button
          type="button"
          role="tab"
          className="tab"
          aria-selected={!unreadOnly}
          onClick={() => setUnreadOnly(false)}
        >
          All
        </button>
        <button
          type="button"
          role="tab"
          className="tab"
          aria-selected={unreadOnly}
          onClick={() => setUnreadOnly(true)}
        >
          Unread{unread > 0 ? ` (${unread})` : ""}
        </button>
      </div>

      {error && (
        <div className="banner banner-error" role="alert">
          <span>{error}</span>
        </div>
      )}

      {loading ? (
        <p className="muted center mt">Loading notifications…</p>
      ) : items.length === 0 ? (
        <div className="panel center">
          <p className="muted" style={{ margin: 0 }}>
            {unreadOnly ? "No unread notifications." : "You have no notifications yet."}
          </p>
        </div>
      ) : (
        <ul className="notif-list">
          {items.map((n) => (
            <li key={n.id}>
              <button
                type="button"
                className={`notif-item${n.read ? "" : " unread"}`}
                onClick={() => void onMarkRead(n)}
                title={n.read ? undefined : "Mark read"}
              >
                {!n.read && <span className="notif-dot" aria-label="Unread" />}
                <span className="notif-text">
                  <span className="notif-title">{n.title}</span>
                  <span className="notif-body">{n.body}</span>
                </span>
                <time className="notif-time" dateTime={n.createdAt}>
                  {relativeTime(n.createdAt)}
                </time>
              </button>
            </li>
          ))}
        </ul>
      )}

      {cursor && !loading && (
        <div className="center mt">
          <button type="button" className="btn outline slim" onClick={() => void load(true)} disabled={busy}>
            {busy ? "Loading…" : "Load more"}
          </button>
        </div>
      )}
    </>
  );
}
