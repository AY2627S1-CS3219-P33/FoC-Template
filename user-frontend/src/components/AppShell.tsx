import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { useAuth } from "../auth/AuthContext";
import { Profile } from "../pages/Profile";
import { Settings } from "../pages/Settings";
import { Placeholder } from "../pages/Placeholder";
import { SuppliersPage } from "../pages/suppliers/SuppliersPage";
import { ErrandsPage } from "../pages/errands/ErrandsPage";
import { CreditPage } from "../pages/credit/CreditPage";
import { NotificationsPage } from "../pages/notifications/NotificationsPage";
import {
  UNREAD_EVENT,
  getUnreadCount,
  publishNotificationsChanged,
  streamUnread,
} from "../api/notification";
import type { Profile as ProfileData } from "../api/types";

type Page = "suppliers" | "errands" | "credits" | "notifications" | "chat" | "profile" | "settings";

const NOTIFICATION_AUDIENCE =
  import.meta.env.VITE_NOTIFICATION_AUDIENCE || "https://api.foc.local/notification-service";

interface NavDef {
  key: Page;
  label: string;
  icon: string;
  // Suppliers, Errands, Credits, Profile and Settings are implemented here;
  // Chat reproduces the wireframe shell but is owned by another service.
  owned: boolean;
}

const NAV: NavDef[] = [
  { key: "suppliers", label: "Suppliers", icon: "⌂", owned: true },
  { key: "errands", label: "Errands", icon: "☰", owned: true },
  { key: "credits", label: "Credits", icon: "◈", owned: true },
  { key: "notifications", label: "Notifications", icon: "🔔", owned: true },
  { key: "chat", label: "Chat", icon: "💬", owned: false },
  { key: "profile", label: "Profile", icon: "◑", owned: true },
  { key: "settings", label: "Settings", icon: "⚙", owned: true },
];

function creditText(balance: number | null): string {
  return balance === null ? "—" : String(balance);
}

function initial(account: ProfileData): string {
  const base = account.display_name || account.username || "?";
  return base.trim().charAt(0).toUpperCase();
}

export function AppShell() {
  const { profile, signOut, getAccessToken } = useAuth();
  const account = profile as ProfileData;
  const isAdmin = account.role === "ADMIN" || account.role === "SUPER_ADMIN";
  const [page, setPage] = useState<Page>("suppliers");
  const [unread, setUnread] = useState(0);

  const nav = NAV;

  const getNotificationToken = useCallback(
    () =>
      getAccessToken({
        audience: NOTIFICATION_AUDIENCE,
        scope: "openid profile email notifications:read:own",
      }),
    [getAccessToken],
  );

  // The unread badge for the nav and the header bell is delivered by server
  // push: streamUnread holds a connection open and reports the count on connect
  // and on every change, so the badge is live without polling. A one-time fetch
  // paints the badge immediately in case the first stream frame is slow, then
  // the stream takes over. Failures are swallowed: a missing badge must never
  // break the shell, and the Notifications page surfaces any real outage.
  useEffect(() => {
    let active = true;
    void (async () => {
      try {
        const count = await getUnreadCount(await getNotificationToken());
        if (active) setUnread(count);
      } catch {
        // ignore; the stream will deliver the count shortly
      }
    })();
    // The stream's first value just seeds the badge on connect; any later value
    // means a notification was recorded, so tell an open notifications page to
    // refresh its list, not only the badge.
    let streamSeeded = false;
    const stream = streamUnread(getNotificationToken, (count) => {
      if (!active) return;
      setUnread(count);
      if (streamSeeded) publishNotificationsChanged();
      streamSeeded = true;
    });
    return () => {
      active = false;
      stream.close();
    };
  }, [getNotificationToken]);

  // Marking notifications read is a client action, not a server event, so the
  // Notifications page announces the new count on UNREAD_EVENT; keep the badge
  // in step with it.
  useEffect(() => {
    const onUnread = (e: Event) => {
      const detail = (e as CustomEvent<number>).detail;
      if (typeof detail === "number") setUnread(detail);
    };
    window.addEventListener(UNREAD_EVENT, onUnread);
    return () => window.removeEventListener(UNREAD_EVENT, onUnread);
  }, []);

  const unreadBadge = unread > 99 ? "99+" : String(unread);

  function render() {
    switch (page) {
      case "suppliers":
        return <SuppliersPage />;
      case "errands":
        return <ErrandsPage />;
      case "credits":
        return <CreditPage />;
      case "notifications":
        return <NotificationsPage />;
      case "profile":
        return <Profile />;
      case "settings":
        return <Settings />;
      default:
        return <Placeholder name={nav.find((n) => n.key === page)?.label ?? ""} />;
    }
  }

  return (
    <div className="app">
      <aside className="sidebar">
        <div className="sidebar-brand">
          <span className="mini-logo">logo</span>
          <span>Friend on Campus</span>
        </div>
        <nav className="nav">
          {nav.map((item) => (
            <button
              key={item.key}
              className="nav-item"
              aria-current={page === item.key ? "page" : undefined}
              disabled={!item.owned}
              title={item.owned ? undefined : "Handled by another service"}
              onClick={() => item.owned && setPage(item.key)}
            >
              <span className="ico" aria-hidden>
                {item.icon}
              </span>
              {item.label}
              {item.key === "notifications" && unread > 0 && (
                <span className="nav-badge" aria-label={`${unread} unread`}>
                  {unreadBadge}
                </span>
              )}
            </button>
          ))}
          {isAdmin && (
            <Link to="/admin" className="nav-item">
              <span className="ico" aria-hidden>
                ⚖
              </span>
              Admin
            </Link>
          )}
        </nav>
        <div className="sidebar-foot">
          <span className="avatar filled">{initial(account)}</span>
          <div className="who">
            <b>{account.username}</b>
            <button type="button" className="link-btn" onClick={signOut}>
              Log out
            </button>
          </div>
        </div>
      </aside>

      <div className="main">
        <header className="topbar">
          <span className="sidebar-brand" style={{ padding: 0 }}>
            <span className="mini-logo">logo</span>
          </span>
          <div className="topbar-actions">
            <button
              type="button"
              className="bell"
              title={unread > 0 ? `${unread} unread notifications` : "Notifications"}
              aria-label={unread > 0 ? `Notifications, ${unread} unread` : "Notifications"}
              onClick={() => setPage("notifications")}
            >
              <span aria-hidden>🔔</span>
              {unread > 0 && <span className="bell-badge">{unreadBadge}</span>}
            </button>
            <button
              type="button"
              className="credit-chip"
              title="Available credits"
              onClick={() => setPage("credits")}
            >
              <span className="b">b</span>
              {creditText(account.credit_balance)}
            </button>
          </div>
        </header>

        <div className="content">{render()}</div>

        <nav className="bottom-nav">
          {nav.map((item) => (
            <button
              key={item.key}
              className="bn-item"
              aria-current={page === item.key ? "page" : undefined}
              disabled={!item.owned}
              onClick={() => item.owned && setPage(item.key)}
            >
              <span className="ico" aria-hidden>
                {item.icon}
                {item.key === "notifications" && unread > 0 && (
                  <span className="bn-badge" aria-hidden>
                    {unreadBadge}
                  </span>
                )}
              </span>
              {item.label}
            </button>
          ))}
          {isAdmin && (
            <Link to="/admin" className="bn-item">
              <span className="ico" aria-hidden>
                ⚖
              </span>
              Admin
            </Link>
          )}
        </nav>
      </div>
    </div>
  );
}
