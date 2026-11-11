// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: User-frontend admin page and AdminLayout behavior.
// Author review: I validated correctness, edited for style, and added boundary checks.

import { Link, NavLink, Outlet } from "react-router-dom";
import { useAuth } from "../../auth/AuthContext";
import type { Profile } from "../../api/types";

export interface AdminNavItem {
  to: string;
  label: string;
  icon: string;
}

// Only screens whose backend exists belong here.
export const ADMIN_NAV: AdminNavItem[] = [
  { to: "/admin/overview", label: "Overview", icon: "◧" },
  { to: "/admin/metrics", label: "Metrics", icon: "▥" },
  { to: "/admin/reviews", label: "Reviews", icon: "⚖" },
  { to: "/admin/events", label: "Events", icon: "☰" },
  { to: "/admin/flags", label: "Flags", icon: "⚑" },
  { to: "/admin/outbox", label: "Outbox", icon: "⇄" },
  { to: "/admin/settings", label: "Settings", icon: "⚙" },
  { to: "/admin/users", label: "Users", icon: "◑" },
  { to: "/admin/suppliers", label: "Suppliers", icon: "⌂" },
  { to: "/admin/lookups", label: "Lookups", icon: "◈" },
];

function initial(account: Profile): string {
  const base = account.display_name || account.username || "?";
  return base.trim().charAt(0).toUpperCase();
}

export function AdminLayout() {
  const { profile, signOut } = useAuth();
  const account = profile as Profile;

  return (
    <div className="app">
      <aside className="sidebar">
        <div className="sidebar-brand">
          <span className="mini-logo">logo</span>
          <span>Admin</span>
        </div>
        <nav className="nav" aria-label="Admin">
          {ADMIN_NAV.map((item) => (
            <NavLink key={item.to} to={item.to} className="nav-item">
              <span className="ico" aria-hidden>
                {item.icon}
              </span>
              {item.label}
            </NavLink>
          ))}
        </nav>
        <Link to="/" className="nav-item admin-back-link">
          <span className="ico" aria-hidden>
            ←
          </span>
          Back to app
        </Link>
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
            <span>Admin</span>
          </span>
          <Link to="/" className="link-btn">
            Back to app
          </Link>
        </header>

        <div className="content content-wide">
          <Outlet />
        </div>

        <nav className="bottom-nav admin-bottom-nav" aria-label="Admin">
          {ADMIN_NAV.map((item) => (
            <NavLink key={item.to} to={item.to} className="bn-item">
              <span className="ico" aria-hidden>
                {item.icon}
              </span>
              {item.label}
            </NavLink>
          ))}
        </nav>
      </div>
    </div>
  );
}
