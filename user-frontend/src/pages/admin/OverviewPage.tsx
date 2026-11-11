// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: User-frontend admin page and OverviewPage behavior.
// Author review: I validated correctness, edited for style, and added boundary checks.

import { useCallback, useEffect, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { getOverview, type AdminOverview } from "../../api/order";
import { errandError } from "../errands/messages";
import { formatDateTime } from "../errands/format";
import { ReadinessPanel } from "./ReadinessPanel";

interface Props {
  getToken: () => Promise<string>;
  /** A token for the admin-service audience, for the readiness section. */
  getAdminToken: () => Promise<string>;
}

// A coarse age: "3 d 4 h", "1 h 30 min", "12 min", "40 s".
function formatAge(totalSeconds: number): string {
  const seconds = Math.max(0, Math.floor(totalSeconds));
  const days = Math.floor(seconds / 86400);
  const hours = Math.floor((seconds % 86400) / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  if (days > 0) return hours > 0 ? `${days} d ${hours} h` : `${days} d`;
  if (hours > 0) return minutes > 0 ? `${hours} h ${minutes} min` : `${hours} h`;
  if (minutes > 0) return `${minutes} min`;
  return `${seconds} s`;
}

interface CounterProps {
  label: string;
  // Undefined until the first load succeeds.
  value: number | undefined;
  detail?: string;
  to?: string;
  error: string | null;
  loading: boolean;
}

// Each counter owns its own error text, so a failed load shows where the
// number should be and the rest of the page (title, refresh, other panels)
// keeps working. A refresh that fails keeps the last good number, dimmed.
function Counter({ label, value, detail, to, error, loading }: CounterProps) {
  const stale = error !== null && value !== undefined;
  const body: ReactNode = (
    <>
      <span className="overview-label">{label}</span>
      <span className={`overview-value${stale ? " is-stale" : ""}`}>
        {value === undefined ? (loading ? "…" : "—") : value}
      </span>
      {error ? (
        <span className="overview-error" role="alert">
          {error}
        </span>
      ) : (
        detail && <span className="overview-detail">{detail}</span>
      )}
    </>
  );
  return to ? (
    <Link to={to} className="overview-card overview-link">
      {body}
    </Link>
  ) : (
    <div className="overview-card">{body}</div>
  );
}

// The live numbers behind GET /admin/overview. They are read when the page
// opens and when Refresh is pressed; nothing polls.
export function OverviewPage({ getToken, getAdminToken }: Props) {
  const [overview, setOverview] = useState<AdminOverview | undefined>();
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const token = await getToken();
      setOverview(await getOverview(token));
    } catch (err) {
      setError(errandError(err));
    } finally {
      setLoading(false);
    }
  }, [getToken]);

  useEffect(() => {
    void load();
  }, [load]);

  const reviews = overview?.openReviews;
  const oldest =
    reviews && reviews.count > 0 && reviews.oldestAgeSeconds !== null
      ? `Oldest waiting ${formatAge(reviews.oldestAgeSeconds)}`
      : reviews
        ? "Nothing waiting"
        : undefined;

  return (
    <>
      <div className="toolbar overview-toolbar">
        <button type="button" className="btn outline slim" onClick={load} disabled={loading}>
          {loading ? "Refreshing…" : "Refresh"}
        </button>
        <span className="muted">
          {overview ? `As of ${formatDateTime(overview.asOf)}` : loading ? "Loading…" : ""}
        </span>
      </div>

      <div className="overview-grid">
        <Counter
          label="Open reviews"
          value={reviews?.count}
          detail={oldest}
          to="/admin/reviews"
          error={error}
          loading={loading}
        />
        <Counter
          label="Ongoing errands"
          value={overview?.ongoing}
          detail="Assigned, picked up or delivered"
          error={error}
          loading={loading}
        />
        <Counter
          label="Waiting for a courier"
          value={overview?.waitingForCourier}
          detail="Open errands nobody has accepted"
          error={error}
          loading={loading}
        />
      </div>

      <div className="viz-layout overview-readiness">
        <ReadinessPanel getToken={getAdminToken} />
      </div>
    </>
  );
}
