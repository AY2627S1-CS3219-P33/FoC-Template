// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: User-frontend admin page and AdminRoutes behavior.
// Author review: I validated correctness, edited for style, and added boundary checks.

import { useCallback, type ReactNode } from "react";
import { Navigate, Route, Routes } from "react-router-dom";
import { useAuth } from "../../auth/AuthContext";
import type { Profile } from "../../api/types";
import { SuppliersPage } from "../suppliers/SuppliersPage";
import { useSupplierToken } from "../suppliers/useSupplierToken";
import { AdminLayout } from "./AdminLayout";
import { AdminUsersPage } from "./AdminUsersPage";
import { ActivityFlags } from "./ActivityFlags";
import { ErrandEvents } from "./ErrandEvents";
import { FailedOutbox } from "./FailedOutbox";
import { LookupsPage } from "./LookupsPage";
import { MetricsPage } from "./MetricsPage";
import { OverviewPage } from "./OverviewPage";
import { OrderSettingsForm } from "./OrderSettingsForm";
import { ReviewQueue } from "./ReviewQueue";

const ORDER_AUDIENCE =
  import.meta.env.VITE_ORDER_AUDIENCE || "https://api.foc.local/order-service";
const ADMIN_AUDIENCE =
  import.meta.env.VITE_ADMIN_AUDIENCE || "https://api.foc.local/admin-service";

function Section({ title, blurb, children }: { title: string; blurb: string; children: ReactNode }) {
  return (
    <>
      <div className="page-head">
        <div>
          <h1>{title}</h1>
          <p className="muted" style={{ margin: "0.3rem 0 0" }}>
            {blurb}
          </p>
        </div>
      </div>
      {children}
    </>
  );
}

function LookupsRoute() {
  const getToken = useSupplierToken();
  return (
    <Section title="Buildings and types" blurb="The lists suppliers are filed under.">
      <LookupsPage getToken={getToken} />
    </Section>
  );
}

export default function AdminRoutes() {
  const { profile, getAccessToken } = useAuth();
  const account = profile as Profile;
  const isAdmin = account.role === "ADMIN" || account.role === "SUPER_ADMIN";

  // Every order-service route here requires errands:review, which is a
  // different permission from the errands:use the user screens ask for.
  const getOrderToken = useCallback(
    () =>
      getAccessToken({
        audience: ORDER_AUDIENCE,
        scope: "openid profile email errands:review",
      }),
    [getAccessToken],
  );

  // The dashboard's metrics and readiness come from admin-service, a separate
  // API with its own permission, dashboard:read.
  const getAdminToken = useCallback(
    () =>
      getAccessToken({
        audience: ADMIN_AUDIENCE,
        scope: "openid profile email dashboard:read",
      }),
    [getAccessToken],
  );

  // UX only: each API still enforces its own permission.
  if (!isAdmin) return <Navigate to="/" replace />;

  return (
    <Routes>
      <Route element={<AdminLayout />}>
        <Route index element={<Navigate to="overview" replace />} />
        <Route
          path="overview"
          element={
            <Section title="Overview" blurb="What needs attention right now.">
              <OverviewPage getToken={getOrderToken} getAdminToken={getAdminToken} />
            </Section>
          }
        />
        <Route
          path="metrics"
          element={
            <Section
              title="Metrics"
              blurb="How the service is being used: errands, couriers, suppliers and what they earn."
            >
              <MetricsPage getToken={getAdminToken} />
            </Section>
          }
        />
        <Route
          path="reviews"
          element={
            <Section
              title="Reviews"
              blurb="Errands waiting on a decision: pay the courier, or refund the requester."
            >
              <ReviewQueue getToken={getOrderToken} />
            </Section>
          }
        />
        <Route
          path="events"
          element={
            <Section title="Events" blurb="The transition log for every errand, newest first.">
              <ErrandEvents getToken={getOrderToken} />
            </Section>
          }
        />
        <Route
          path="flags"
          element={
            <Section
              title="Flags"
              blurb="Users whose withdrawals or cancellations crossed the configured thresholds."
            >
              <ActivityFlags getToken={getOrderToken} />
            </Section>
          }
        />
        <Route
          path="outbox"
          element={
            <Section title="Outbox" blurb="Credit commands and events that failed and need replaying.">
              <FailedOutbox getToken={getOrderToken} />
            </Section>
          }
        />
        <Route
          path="settings"
          element={
            <Section
              title="Settings"
              blurb="Deadlines and the thresholds that restrict couriers and flag requesters."
            >
              <OrderSettingsForm getToken={getOrderToken} />
            </Section>
          }
        />
        <Route path="users" element={<AdminUsersPage />} />
        <Route path="suppliers" element={<SuppliersPage adminMode />} />
        <Route path="lookups" element={<LookupsRoute />} />
        <Route path="*" element={<Navigate to="/admin/overview" replace />} />
      </Route>
    </Routes>
  );
}
