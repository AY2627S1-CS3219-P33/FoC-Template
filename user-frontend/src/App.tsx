import { AuthProvider, useAuth } from "./auth/AuthContext";
import { AuthScreen } from "./pages/AuthScreen";
import { AppShell } from "./components/AppShell";
import { AdminUsersPage } from "./pages/admin/AdminUsersPage";

function Routed() {
  const { status } = useAuth();

  if (status === "loading") {
    return (
      <main className="auth-shell">
        <p className="muted">Loading…</p>
      </main>
    );
  }

  if (status !== "signedIn") return <AuthScreen />;
  return window.location.pathname === "/admin" ? <AdminUsersPage /> : <AppShell />;
}

export function App() {
  return (
    <AuthProvider>
      <Routed />
    </AuthProvider>
  );
}
