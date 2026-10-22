// Client-side mirror of the Order Service field contract
// (order-service/openapi/common.yaml ErrandCreate and the schemas it
// references). The service remains the source of truth; these checks give
// immediate feedback and match its limits so a submission is not rejected for
// something the form could have caught.

export const LIMITS = {
  itemDescription: 200,
  itemQuantity: 99,
  items: 20,
  deliveryDetail: 200,
  notes: 500,
  contactName: 100,
  contactMobile: 32,
  reason: 500,
} as const;

// An acceptance deadline must be 10 minutes to 24 hours from now, inclusive.
export const DEADLINE_MIN_MS = 10 * 60 * 1000;
export const DEADLINE_MAX_MS = 24 * 60 * 60 * 1000;
// Default: one hour after creation, as the service does when it is omitted.
export const DEADLINE_DEFAULT_MS = 60 * 60 * 1000;

// Control characters are rejected for the same reason user-service rejects them
// in a display name or mobile number.
const CONTROL_RE = /[\u0000-\u001f\u007f]/;

export interface ItemDraft {
  description: string;
  quantity: string;
}

export interface ErrandDraft {
  supplierId: string;
  deliveryBuildingId: string;
  deliveryDetail: string;
  items: ItemDraft[];
  notes: string;
  contactName: string;
  contactMobile: string;
  reward: string;
  acceptanceDeadline: string; // datetime-local value
}

export interface ErrandFormErrors {
  supplierId?: string;
  deliveryBuildingId?: string;
  deliveryDetail?: string;
  items?: string;
  notes?: string;
  contactName?: string;
  contactMobile?: string;
  reward?: string;
  acceptanceDeadline?: string;
  // One message per item row, indexed as the draft's items.
  itemErrors?: (string | undefined)[];
}

function requiredText(value: string, label: string, max: number): string | null {
  const trimmed = value.trim();
  if (trimmed.length === 0) return `${label} is required.`;
  if (trimmed.length > max) return `${label} must be at most ${max} characters.`;
  if (CONTROL_RE.test(value)) return `${label} must not contain control characters.`;
  return null;
}

// Validates the string-based form draft. Quantities, the reward and the
// deadline arrive as strings from the inputs.
export function validateErrandForm(draft: ErrandDraft, now = Date.now()): ErrandFormErrors {
  const errors: ErrandFormErrors = {};

  if (draft.supplierId === "") errors.supplierId = "Choose where the items are picked up.";
  if (draft.deliveryBuildingId === "")
    errors.deliveryBuildingId = "Choose the delivery building.";

  const detail = requiredText(draft.deliveryDetail, "Delivery detail", LIMITS.deliveryDetail);
  if (detail) errors.deliveryDetail = detail;

  const name = requiredText(draft.contactName, "Contact name", LIMITS.contactName);
  if (name) errors.contactName = name;
  const mobile = requiredText(draft.contactMobile, "Contact mobile", LIMITS.contactMobile);
  if (mobile) errors.contactMobile = mobile;

  if (draft.notes.length > LIMITS.notes)
    errors.notes = `Notes must be at most ${LIMITS.notes} characters.`;

  // Reward is a positive integer number of credits (F3.1.1).
  const reward = draft.reward.trim();
  if (reward === "") errors.reward = "Reward is required.";
  else if (!/^\d+$/.test(reward)) errors.reward = "Reward must be a whole number of credits.";
  else if (Number(reward) < 1) errors.reward = "Reward must be at least 1 credit.";

  if (draft.items.length === 0) errors.items = "Add at least one item.";
  else if (draft.items.length > LIMITS.items)
    errors.items = `At most ${LIMITS.items} items.`;

  const itemErrors = draft.items.map((item) => {
    const description = requiredText(item.description, "Item", LIMITS.itemDescription);
    if (description) return description;
    const quantity = item.quantity.trim();
    if (quantity === "") return "Quantity is required.";
    if (!/^\d+$/.test(quantity)) return "Quantity must be a whole number.";
    const value = Number(quantity);
    if (value < 1 || value > LIMITS.itemQuantity)
      return `Quantity must be between 1 and ${LIMITS.itemQuantity}.`;
    return undefined;
  });
  if (itemErrors.some(Boolean)) errors.itemErrors = itemErrors;

  // An empty deadline is allowed: the service defaults it to one hour out.
  if (draft.acceptanceDeadline !== "") {
    const at = new Date(draft.acceptanceDeadline).getTime();
    if (!Number.isFinite(at)) errors.acceptanceDeadline = "Enter a valid date and time.";
    else if (at - now < DEADLINE_MIN_MS)
      errors.acceptanceDeadline = "Must be at least 10 minutes from now.";
    else if (at - now > DEADLINE_MAX_MS)
      errors.acceptanceDeadline = "Must be at most 24 hours from now.";
  }

  return errors;
}

export function hasErrandErrors(errors: ErrandFormErrors): boolean {
  return Object.keys(errors).length > 0;
}

// A required free-text reason, as dispute, report-issue, resolve and requeue
// all take.
export function validateReason(value: string): string | null {
  const trimmed = value.trim();
  if (trimmed.length === 0) return "A reason is required.";
  if (trimmed.length > LIMITS.reason)
    return `Must be at most ${LIMITS.reason} characters.`;
  return null;
}

// ---- datetime-local helpers --------------------------------------------------
//
// A datetime-local input has no timezone; it reads and writes local wall-clock
// time, while the contract uses RFC 3339 instants.

export function toLocalInput(date: Date): string {
  const pad = (n: number) => String(n).padStart(2, "0");
  return (
    `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}` +
    `T${pad(date.getHours())}:${pad(date.getMinutes())}`
  );
}

export function fromLocalInput(value: string): string | undefined {
  if (value === "") return undefined;
  const at = new Date(value);
  return Number.isFinite(at.getTime()) ? at.toISOString() : undefined;
}

export function defaultDeadline(now = Date.now()): string {
  return toLocalInput(new Date(now + DEADLINE_DEFAULT_MS));
}
