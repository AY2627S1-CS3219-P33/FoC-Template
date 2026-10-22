// Presentation helpers shared by the errand screens. Nothing here decides what
// an errand may do — the service does, and ErrandDetail reads the status and
// viewerRole it returns.

import type { ErrandStatus, Item, ReviewReason } from "../../api/order";

const STATUS_LABELS: Record<ErrandStatus, string> = {
  PENDING_RESERVATION: "Reserving credits",
  OPEN: "Open",
  ASSIGNED: "Assigned",
  PICKED_UP: "Picked up",
  DELIVERED: "Delivered",
  REVIEW_REQUIRED: "In review",
  REJECTED: "Rejected",
  CANCELLED: "Cancelled",
  EXPIRED: "Expired",
  COMPLETED: "Completed",
  REFUNDED: "Refunded",
};

// Tone drives the chip colour: live work in progress, a good ending, a bad
// ending, or something waiting on a person or a dependency.
type Tone = "progress" | "good" | "bad" | "waiting";

const STATUS_TONES: Record<ErrandStatus, Tone> = {
  PENDING_RESERVATION: "waiting",
  OPEN: "progress",
  ASSIGNED: "progress",
  PICKED_UP: "progress",
  DELIVERED: "waiting",
  REVIEW_REQUIRED: "waiting",
  REJECTED: "bad",
  CANCELLED: "bad",
  EXPIRED: "bad",
  COMPLETED: "good",
  REFUNDED: "bad",
};

export function statusLabel(status: ErrandStatus): string {
  return STATUS_LABELS[status] ?? status;
}

export function statusClass(status: ErrandStatus): string {
  return `status-chip tone-${STATUS_TONES[status] ?? "waiting"}`;
}

const REVIEW_REASONS: Record<ReviewReason, string> = {
  COURIER_ISSUE: "Courier reported a problem",
  REQUESTER_DISPUTE: "Requester disputed the delivery",
  DELIVERY_OVERDUE: "Delivery overdue",
};

export function reviewReasonLabel(reason: ReviewReason): string {
  return REVIEW_REASONS[reason] ?? reason;
}

// "3 x Iced latte, 1 x Croissant"
export function itemSummary(items: Item[]): string {
  return items.map((item) => `${item.quantity} × ${item.description}`).join(", ");
}

export function itemCount(items: Item[]): number {
  return items.reduce((total, item) => total + item.quantity, 0);
}

export function formatDateTime(iso: string): string {
  const at = new Date(iso);
  if (!Number.isFinite(at.getTime())) return iso;
  return at.toLocaleString(undefined, {
    day: "numeric",
    month: "short",
    hour: "2-digit",
    minute: "2-digit",
  });
}

// Time left until a deadline, or how long ago it passed. Deliberately coarse:
// these screens are polled, not live, so a to-the-second countdown would
// promise more freshness than the data has.
export function timeLeft(iso: string, now = Date.now()): string {
  const at = new Date(iso).getTime();
  if (!Number.isFinite(at)) return "";
  const ms = at - now;
  const past = ms < 0;
  const minutes = Math.floor(Math.abs(ms) / 60000);
  let text: string;
  if (minutes < 1) text = "under a minute";
  else if (minutes < 60) text = `${minutes} min`;
  else {
    const hours = Math.floor(minutes / 60);
    const rest = minutes % 60;
    if (hours < 24) text = rest === 0 ? `${hours} h` : `${hours} h ${rest} min`;
    else text = `${Math.floor(hours / 24)} d`;
  }
  return past ? `${text} ago` : `in ${text}`;
}

export function formatDistance(meters: number): string {
  return meters < 1000 ? `${Math.round(meters)} m` : `${(meters / 1000).toFixed(1)} km`;
}

// Settings durations are whole seconds; the admin form shows them in the
// largest unit that divides evenly so 7200 reads as "2 h".
export function formatDuration(seconds: number): string {
  if (seconds % 86400 === 0 && seconds >= 86400) return `${seconds / 86400} d`;
  if (seconds % 3600 === 0 && seconds >= 3600) return `${seconds / 3600} h`;
  if (seconds % 60 === 0 && seconds >= 60) return `${seconds / 60} min`;
  return `${seconds} s`;
}

export function creditText(amount: number): string {
  return `${amount} ${amount === 1 ? "credit" : "credits"}`;
}
