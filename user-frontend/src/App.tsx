import { AuthProvider, useAuth } from "./auth/AuthContext";
import { AuthScreen } from "./pages/AuthScreen";
import { AppShell } from "./components/AppShell";

function Routed() {
  const { status } = useAuth();

  if (status === "loading") {
    return (
      <main className="auth-shell">
        <p className="muted">Loading…</p>
      </main>
    );
  }

  return status === "signedIn" ? <AppShell /> : <AuthScreen />;
}

export function App() {
  return (
    <AuthProvider>
      <Routed />
    </AuthProvider>
  );
}
