// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: User-frontend API client for admin service data.
// Author review: I validated correctness, edited for style, and added boundary checks.

// Admin Service client. Shapes mirror admin-service/openapi.yaml. The service
// is reached at /admin-service (its documented gateway route); in dev the Vite
// proxy forwards there. Every route needs a login token for the admin-service
// audience with dashboard:read, and answers with asOf.

const BASE = "/admin-service";

export type AdminErrorCode =
  | "INVALID_ARGUMENT"
  | "UNAUTHENTICATED"
  | "FORBIDDEN"
  | "DEPENDENCY_UNAVAILABLE"
  | "INTERNAL";

export class AdminError extends Error {
  readonly status: number;
  readonly code: AdminErrorCode | string;

  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = "AdminError";
    this.status = status;
    this.code = code;
  }
}

// ---- the days a period covers ------------------------------------------------

// The dashboard counts days in this zone (admin-service DASHBOARD_TIMEZONE,
// default Asia/Singapore). The server repeats the zone it used as `timezone`,
// and the dates it answers for are the ones to show.
export const DASHBOARD_TIMEZONE = "Asia/Singapore";

/** Today's date in the dashboard's zone, YYYY-MM-DD. */
export function today(now: Date = new Date()): string {
  return new Intl.DateTimeFormat("en-CA", {
    timeZone: DASHBOARD_TIMEZONE,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  }).format(now);
}

/** A date YYYY-MM-DD moved by whole days. Calendar arithmetic, so no zone matters. */
export function addDays(date: string, days: number): string {
  const moved = new Date(`${date}T00:00:00Z`);
  moved.setUTCDate(moved.getUTCDate() + days);
  return moved.toISOString().slice(0, 10);
}

/** Both ends included: the last `days` days ending today. */
export function lastDays(days: number, now: Date = new Date()): Period {
  const to = today(now);
  return { from: addDays(to, -(days - 1)), to };
}

export interface Period {
  from: string;
  to: string;
}

// ---- responses ---------------------------------------------------------------

interface Header extends Period {
  asOf: string;
  timezone: string;
}

export interface ErrandsPerDay extends Header {
  days: { date: string; created: number }[];
}

export interface TimeToAccept extends Header {
  accepted: number;
  medianSeconds: number | null;
  p90Seconds: number | null;
}

export type OutcomeStatus = "COMPLETED" | "CANCELLED" | "EXPIRED" | "REJECTED" | "REFUNDED";

export interface Outcomes extends Header {
  finished: number;
  outcomes: { status: OutcomeStatus; count: number; rate: number }[];
}

export interface Ranking extends Header {
  basis: "finished";
  items: { id: string; name: string | null; finished: number; completed: number }[];
  unattributed: number;
}

export interface ActiveCouriers extends Header {
  couriers: number;
}

export interface RewardVolume extends Header {
  total: number;
  completed: number;
  days: { date: string; reward: number; completed: number }[];
}

export interface ServiceReadiness {
  asOf: string;
  services: {
    name: string;
    state: "up" | "down";
    status?: string;
    dependencies?: Record<string, string>;
    reason?: "unreachable" | "timeout" | "not_ready" | "unexpected_status";
    latencyMillis: number;
  }[];
  notChecked: { name: string; reason: string }[];
}

// ---- HTTP plumbing -----------------------------------------------------------

async function get<T>(token: string, path: string, query: Record<string, string | number | undefined> = {}): Promise<T> {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) {
    if (value !== undefined && value !== "") search.set(key, String(value));
  }
  const qs = search.toString();
  const response = await fetch(`${BASE}${path}${qs ? `?${qs}` : ""}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  const text = await response.text();
  let body: { code?: string; message?: string } = {};
  try {
    body = text ? JSON.parse(text) : {};
  } catch {
    // A gateway error page: fall through to the status-based message.
  }
  if (!response.ok) {
    throw new AdminError(
      response.status,
      body.code ?? "INTERNAL",
      body.message ?? `Request failed with status ${response.status}.`,
    );
  }
  return body as T;
}

export const getErrandsPerDay = (token: string, p: Period) =>
  get<ErrandsPerDay>(token, "/v1/metrics/errands-per-day", { ...p });

export const getTimeToAccept = (token: string, p: Period) =>
  get<TimeToAccept>(token, "/v1/metrics/time-to-accept", { ...p });

export const getOutcomes = (token: string, p: Period) => get<Outcomes>(token, "/v1/metrics/outcomes", { ...p });

export const getTopSuppliers = (token: string, p: Period, limit = 10) =>
  get<Ranking>(token, "/v1/metrics/top-suppliers", { ...p, limit });

export const getTopBuildings = (token: string, p: Period, limit = 10) =>
  get<Ranking>(token, "/v1/metrics/top-buildings", { ...p, limit });

export const getActiveCouriers = (token: string, p: Period) =>
  get<ActiveCouriers>(token, "/v1/metrics/active-couriers", { ...p });

export const getRewardVolume = (token: string, p: Period) =>
  get<RewardVolume>(token, "/v1/metrics/reward-volume", { ...p });

export const getServiceReadiness = (token: string) => get<ServiceReadiness>(token, "/v1/readiness");
