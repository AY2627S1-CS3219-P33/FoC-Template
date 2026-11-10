import { useState } from "react";
import { Link } from "react-router-dom";
import { useAuth } from "../auth/AuthContext";
import { Profile } from "../pages/Profile";
import { Settings } from "../pages/Settings";
import { SuppliersPage } from "../pages/suppliers/SuppliersPage";
import { ErrandsPage } from "../pages/errands/ErrandsPage";
import { CreditPage } from "../pages/credit/CreditPage";
import type { Profile as ProfileData } from "../api/types";
import type { Supplier } from "../api/supplier";

type Page = "suppliers" | "errands" | "credits" | "profile" | "settings";

interface NavDef {
  key: Page;
  label: string;
  icon: string;
}

// Chat has no page of its own: it is a panel on each errand's page.
const NAV: NavDef[] = [
  { key: "suppliers", label: "Suppliers", icon: "⌂" },
  { key: "errands", label: "Errands", icon: "☰" },
  { key: "credits", label: "Credits", icon: "◈" },
  { key: "profile", label: "Profile", icon: "◑" },
  { key: "settings", label: "Settings", icon: "⚙" },
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
  const [navigation, setNavigation] = useState<{ page: Page; supplier?: Supplier }>({
    page: "suppliers",
  });
  const { page } = navigation;

  function setPage(page: Page) {
    setNavigation({ page });
  }

  const nav = NAV;

  function render() {
    switch (page) {
      case "suppliers":
        return (
          <SuppliersPage
            onRequestErrand={(supplier) => setNavigation({ page: "errands", supplier })}
          />
        );
      case "errands":
        return <ErrandsPage initialSupplier={navigation.supplier} />;
      case "credits":
        return <CreditPage />;
      case "profile":
        return <Profile />;
      case "settings":
        return <Settings />;
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
              onClick={() => setPage(item.key)}
            >
              <span className="ico" aria-hidden>
                {item.icon}
              </span>
              {item.label}
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
          {nav.map((item) => (
            <button
              key={item.key}
              className="bn-item"
              aria-current={page === item.key ? "page" : undefined}
              onClick={() => setPage(item.key)}
            >
              <span className="ico" aria-hidden>
                {item.icon}
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
