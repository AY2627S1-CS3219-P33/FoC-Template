// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: User-frontend application routes and page composition.
// Author review: I validated correctness, edited for style, and added boundary checks.

import { lazy, Suspense } from "react";
import { Route, Routes } from "react-router-dom";
import { AuthProvider, useAuth } from "./auth/AuthContext";
import { AuthScreen } from "./pages/AuthScreen";
import { AppShell } from "./components/AppShell";

// The admin area is its own chunk, so ordinary users never download it.
const AdminRoutes = lazy(() => import("./pages/admin/AdminRoutes"));

function Loading() {
  return (
    <main className="auth-shell">
      <p className="muted">Loading…</p>
    </main>
  );
}

function Routed() {
  const { status } = useAuth();

  if (status === "loading") return <Loading />;
  if (status !== "signedIn") return <AuthScreen />;

  return (
    <Suspense fallback={<Loading />}>
      <Routes>
        <Route path="/admin/*" element={<AdminRoutes />} />
        <Route path="*" element={<AppShell />} />
      </Routes>
    </Suspense>
  );
}

export function App() {
  return (
    <AuthProvider>
      <Routed />
    </AuthProvider>
  );
}
