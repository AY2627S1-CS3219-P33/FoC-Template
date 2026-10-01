import { useState } from "react";
import { useAuth } from "../auth/AuthContext";
import { Profile } from "../pages/Profile";
import { Settings } from "../pages/Settings";
import { Placeholder } from "../pages/Placeholder";
import { SuppliersPage } from "../pages/suppliers/SuppliersPage";
import type { Profile as ProfileData } from "../api/types";

type Page = "suppliers" | "errands" | "chat" | "profile" | "settings";

interface NavDef {
  key: Page;
  label: string;
  icon: string;
  // Suppliers (User + Supplier services), Profile and Settings are implemented
  // here; the rest reproduce the wireframe shell but are owned by other services.
  owned: boolean;
}

const NAV: NavDef[] = [
  { key: "suppliers", label: "Suppliers", icon: "⌂", owned: true },
  { key: "errands", label: "Errands", icon: "☰", owned: false },
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
  const { profile, signOut } = useAuth();
  const account = profile as ProfileData;
  const [page, setPage] = useState<Page>("suppliers");

  function render() {
    switch (page) {
      case "suppliers":
        return <SuppliersPage />;
      case "profile":
        return <Profile />;
      case "settings":
        return <Settings />;
      default:
        return <Placeholder name={NAV.find((n) => n.key === page)?.label ?? ""} />;
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
          {NAV.map((item) => (
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
            </button>
          ))}
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
          <span className="credit-chip" title="Available credits">
            <span className="b">b</span>
            {creditText(account.credit_balance)}
          </span>
        </header>

        <div className="content">{render()}</div>

        <nav className="bottom-nav">
          {NAV.map((item) => (
            <button
              key={item.key}
              className="bn-item"
              aria-current={page === item.key ? "page" : undefined}
              disabled={!item.owned}
              onClick={() => item.owned && setPage(item.key)}
            >
              <span className="ico" aria-hidden>
                {item.icon}
              </span>
              {item.label}
            </button>
          ))}
        </nav>
      </div>
    </div>
  );
}
