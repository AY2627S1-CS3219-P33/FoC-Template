import { useState } from "react";
import { useAuth } from "../auth/AuthContext";
import { Profile } from "../pages/Profile";
import { Settings } from "../pages/Settings";
import { Placeholder } from "../pages/Placeholder";
import { SuppliersPage } from "../pages/suppliers/SuppliersPage";
import { ErrandsPage } from "../pages/errands/ErrandsPage";
import { CreditPage } from "../pages/credit/CreditPage";
import { OrderAdminPage } from "../pages/admin/OrderAdminPage";
import type { Profile as ProfileData } from "../api/types";

type Page = "suppliers" | "errands" | "credits" | "chat" | "profile" | "settings" | "orderAdmin";

interface NavDef {
  key: Page;
  label: string;
  icon: string;
  // Suppliers, Errands, Credits, Profile and Settings are implemented here;
  // Chat reproduces the wireframe shell but is owned by another service.
  owned: boolean;
  // Errand administration needs the errands:review permission, so it is only
  // offered to accounts with an administrator role.
  adminOnly?: boolean;
}

const NAV: NavDef[] = [
  { key: "suppliers", label: "Suppliers", icon: "⌂", owned: true },
  { key: "errands", label: "Errands", icon: "☰", owned: true },
  { key: "credits", label: "Credits", icon: "◈", owned: true },
  { key: "chat", label: "Chat", icon: "💬", owned: false },
  { key: "profile", label: "Profile", icon: "◑", owned: true },
  { key: "settings", label: "Settings", icon: "⚙", owned: true },
  { key: "orderAdmin", label: "Errand admin", icon: "⚖", owned: true, adminOnly: true },
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
  const isAdmin = account.role === "ADMIN" || account.role === "SUPER_ADMIN";
  const [page, setPage] = useState<Page>("suppliers");

  const nav = NAV.filter((item) => !item.adminOnly || isAdmin);

  function render() {
    switch (page) {
      case "suppliers":
        return <SuppliersPage />;
      case "errands":
        return <ErrandsPage />;
      case "credits":
        return <CreditPage />;
      case "orderAdmin":
        return <OrderAdminPage />;
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
          <button
            type="button"
            className="credit-chip"
            title="Available credits"
            onClick={() => setPage("credits")}
          >
            <span className="b">b</span>
            {creditText(account.credit_balance)}
          </button>
        </header>

        <div className="content">{render()}</div>

        <nav className="bottom-nav">
          {nav
            .filter((item) => item.key !== "orderAdmin")
            .map((item) => (
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
