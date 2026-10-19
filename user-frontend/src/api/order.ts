// Order Service client. Shapes mirror the frozen OpenAPI contract
// (order-service/openapi). The service is reached at /order-service (its
// documented gateway route); in dev the Vite proxy forwards there.
//
// Two contract rules shape every call here:
//   * every action on an existing errand carries expectedVersion, and a stale
//     one is 409 ERRAND_CHANGED;
//   * creation carries a caller-generated Idempotency-Key, reused on retry so
//     an interrupted submission cannot create two errands (NFR7.3).

const BASE = "/order-service";

export type OrderErrorCode =
  | "INVALID_ARGUMENT"
  | "UNAUTHENTICATED"
  | "FORBIDDEN"
  | "VALIDATION_FAILED"
  | "ERRAND_NOT_FOUND"
  | "FENCE_NOT_FOUND"
  | "OUTBOX_ROW_NOT_FOUND"
  | "ERRAND_CHANGED"
  | "ERRAND_UNAVAILABLE"
  | "INVALID_TRANSITION"
  | "NOT_ELIGIBLE"
  | "COURIER_RESTRICTED"
  | "INSUFFICIENT_CREDITS"
  | "EDIT_PENDING"
  | "SUPPLIER_UNAVAILABLE"
  | "SUPPLIER_DELETION_IN_PROGRESS"
  | "DEPENDENCY_UNAVAILABLE"
  | "INTERNAL"
  | "NETWORK";

export interface FieldError {
  field: string;
  message: string;
}

export class OrderError extends Error {
  readonly status: number;
  readonly code: OrderErrorCode;
  readonly fields: FieldError[];
  constructor(status: number, code: OrderErrorCode, message: string, fields: FieldError[] = []) {
    super(message);
    this.name = "OrderError";
    this.status = status;
    this.code = code;
    this.fields = fields;
  }
}

// ---- domain types ------------------------------------------------------------

export type ErrandStatus =
  | "PENDING_RESERVATION"
  | "OPEN"
  | "ASSIGNED"
  | "PICKED_UP"
  | "DELIVERED"
  | "REVIEW_REQUIRED"
  | "REJECTED"
  | "CANCELLED"
  | "EXPIRED"
  | "COMPLETED"
  | "REFUNDED";

// Non-terminal states. The service decides what is active; this mirrors it for
// labelling only, never to filter a list the service already scoped.
export const ACTIVE_STATUSES: readonly ErrandStatus[] = [
  "PENDING_RESERVATION",
  "OPEN",
  "ASSIGNED",
  "PICKED_UP",
  "DELIVERED",
  "REVIEW_REQUIRED",
];

export type ReviewReason = "COURIER_ISSUE" | "REQUESTER_DISPUTE" | "DELIVERY_OVERDUE";
export type ActorType = "REQUESTER" | "COURIER" | "ADMIN" | "SYSTEM";
export type ViewerRole = "REQUESTER" | "COURIER" | "PUBLIC" | "ADMIN";

export type RejectionReason =
  | "INSUFFICIENT_CREDITS"
  | "ACCOUNT_NOT_FOUND"
  | "RESERVATION_RELEASED"
  | "RESERVATION_FAILED";

export interface Item {
  description: string;
  quantity: number;
}

export interface PickupLocation {
  name: string;
  building: string;
  floor: string;
  locationDescription: string;
  latitude: number;
  longitude: number;
}

export interface DeliveryBuilding {
  id: string;
  name: string;
}

// Fields shared by the open-list projection and every participant view.
interface ErrandCore {
  errandId: string;
  version: number;
  supplierId: string;
  supplierVersionId: string;
  pickup: PickupLocation;
  deliveryBuilding: DeliveryBuilding;
  deliveryDetail: string;
  items: Item[];
  reward: number;
  createdAt: string;
  acceptanceDeadline: string;
}

export interface OpenErrandItem extends ErrandCore {
  // Errands sharing a groupKey can be picked up together (F3.2.1). Opaque:
  // the contract forbids parsing it.
  groupKey: string;
  distanceMeters?: number;
}

export interface OpenErrandPage {
  items: OpenErrandItem[];
  nextCursor?: string;
}

// Fields the requester, assigned courier and an admin all see.
interface ErrandFields extends ErrandCore {
  status: ErrandStatus;
  notes: string;
  updatedAt: string;
  pickupDeadline?: string;
  deliveryDeadline?: string;
  deliveredAt?: string;
  verificationDeadline?: string;
  reviewReason?: ReviewReason;
}

export interface PendingEdit {
  supplierId: string;
  supplierVersionId: string;
  pickup: PickupLocation;
  deliveryBuilding: DeliveryBuilding;
  deliveryDetail: string;
  items: Item[];
  notes: string;
  contactName: string;
  contactMobile: string;
  reward: number;
  acceptanceDeadline: string;
  requestedAt: string;
}

export interface RequesterErrandView extends ErrandFields {
  viewerRole: "REQUESTER";
  contactName: string;
  contactMobile: string;
  pendingEdit?: PendingEdit;
  rejectionReason?: RejectionReason;
}

export interface CourierErrandView extends ErrandFields {
  viewerRole: "COURIER";
  // Present only while ASSIGNED, PICKED_UP, DELIVERED or REVIEW_REQUIRED.
  contactName?: string;
  contactMobile?: string;
}

export interface PublicErrandView extends ErrandCore {
  viewerRole: "PUBLIC";
}

export type ErrandView = RequesterErrandView | CourierErrandView | PublicErrandView;
export type ParticipantErrandView = RequesterErrandView | CourierErrandView;

export interface RequesterErrandPage {
  items: RequesterErrandView[];
  nextCursor?: string;
}
export interface CourierErrandPage {
  items: CourierErrandView[];
  nextCursor?: string;
}
export interface HistoryPage {
  items: ParticipantErrandView[];
  nextCursor?: string;
}

export interface ErrandCreate {
  supplierId: string;
  deliveryBuildingId: string;
  deliveryDetail: string;
  items: Item[];
  notes?: string;
  contactName: string;
  contactMobile: string;
  reward: number;
  acceptanceDeadline?: string;
}

// expectedVersion plus at least one field to change; omitted fields keep their
// current value.
export interface ErrandEdit {
  expectedVersion: number;
  supplierId?: string;
  deliveryBuildingId?: string;
  deliveryDetail?: string;
  items?: Item[];
  notes?: string;
  contactName?: string;
  contactMobile?: string;
  reward?: number;
  acceptanceDeadline?: string;
}

// ---- admin types -------------------------------------------------------------

export interface Review {
  reason: ReviewReason;
  reporterId?: string;
  note?: string;
  createdAt: string;
  resolution?: "COMPLETED" | "REFUNDED";
  resolverId?: string;
  resolutionReason?: string;
  resolvedAt?: string;
}

export interface AdminErrandView extends ErrandFields {
  viewerRole: "ADMIN";
  requesterId: string;
  courierId?: string;
  review: Review;
}

export interface AdminReviewPage {
  items: AdminErrandView[];
  nextCursor?: string;
}

export interface WithdrawalRestriction {
  count: number;
  window: number;
  duration: number;
}

export interface CancellationFlag {
  count: number;
  window: number;
}

export interface Settings {
  pickupAfter: number;
  deliveryAfter: number;
  verificationAfter: number;
  verificationReminderAfter: number;
  withdrawalRestriction: WithdrawalRestriction;
  cancellationFlag: CancellationFlag;
  reservationTimeout: number;
  editTimeout: number;
  outboxConfirmTimeout: number;
}

export interface TransitionRecord {
  id: number;
  errandId: string;
  fromStatus?: ErrandStatus;
  toStatus: ErrandStatus;
  event: string;
  actorType: ActorType;
  actorId?: string;
  reason?: string;
  errandVersion: number;
  at: string;
}

export interface TransitionPage {
  items: TransitionRecord[];
  nextCursor?: string;
}

export interface Flag {
  userId: string;
  kind: "COURIER_WITHDRAWALS" | "REQUESTER_CANCELLATIONS";
  count: number;
  threshold: number;
  window: number;
  since: string;
  summary: string;
}

export interface FlagList {
  items: Flag[];
}

export interface OutboxRow {
  id: number;
  kind: string;
  errandId: string;
  status: "PENDING" | "SENT" | "CONFIRMED" | "FAILED";
  attempts: number;
  lastFailureCode?: string;
  createdAt: string;
  nextAttemptAt: string;
  sentAt?: string;
}

export interface OutboxRowPage {
  items: OutboxRow[];
  nextCursor?: string;
}

// ---- HTTP plumbing -----------------------------------------------------------

async function parse<T>(response: Response): Promise<T> {
  const text = await response.text();
  const body = text ? JSON.parse(text) : {};
  if (!response.ok) {
    throw new OrderError(
      response.status,
      body.code ?? "INTERNAL",
      body.message ?? `Request failed with status ${response.status}.`,
      body.fields ?? [],
    );
  }
  return body as T;
}

function headers(token: string, json = false): Record<string, string> {
  const h: Record<string, string> = { Authorization: `Bearer ${token}` };
  if (json) h["Content-Type"] = "application/json";
  return h;
}

function query(params: Record<string, string | number | undefined>): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== "") search.set(key, String(value));
  }
  const qs = search.toString();
  return qs ? `?${qs}` : "";
}

// Every versioned action posts to an action path with the same body shape.
async function action<T>(
  token: string,
  errandId: string,
  verb: string,
  body: Record<string, unknown>,
): Promise<T> {
  return parse<T>(
    await fetch(`${BASE}/errands/${encodeURIComponent(errandId)}/${verb}`, {
      method: "POST",
      headers: headers(token, true),
      body: JSON.stringify(body),
    }),
  );
}

// ---- errands -----------------------------------------------------------------

export interface ListOpenParams {
  supplierId?: string;
  lat?: number;
  lng?: number;
  limit?: number;
  cursor?: string;
}

export async function listOpenErrands(
  token: string,
  params: ListOpenParams,
): Promise<OpenErrandPage> {
  return parse<OpenErrandPage>(
    await fetch(`${BASE}/errands/open${query({ ...params })}`, { headers: headers(token) }),
  );
}

export async function getErrand(token: string, errandId: string): Promise<ErrandView> {
  return parse<ErrandView>(
    await fetch(`${BASE}/errands/${encodeURIComponent(errandId)}`, { headers: headers(token) }),
  );
}

// createErrand answers 202 while the credit reservation is in progress and 200
// when replaying an earlier request with the same key; both return the errand.
export async function createErrand(
  token: string,
  body: ErrandCreate,
  idempotencyKey: string,
): Promise<RequesterErrandView> {
  return parse<RequesterErrandView>(
    await fetch(`${BASE}/errands`, {
      method: "POST",
      headers: { ...headers(token, true), "Idempotency-Key": idempotencyKey },
      body: JSON.stringify(body),
    }),
  );
}

// editErrand answers 200 when applied at once and 202 when a reward change is
// waiting for credit-service, in which case the result carries pendingEdit.
export async function editErrand(
  token: string,
  errandId: string,
  body: ErrandEdit,
): Promise<{ view: RequesterErrandView; pending: boolean }> {
  const response = await fetch(`${BASE}/errands/${encodeURIComponent(errandId)}`, {
    method: "PATCH",
    headers: headers(token, true),
    body: JSON.stringify(body),
  });
  const view = await parse<RequesterErrandView>(response);
  return { view, pending: response.status === 202 };
}

export function acceptErrand(
  token: string,
  errandId: string,
  expectedVersion: number,
): Promise<CourierErrandView> {
  return action<CourierErrandView>(token, errandId, "accept", { expectedVersion });
}

export function cancelErrand(
  token: string,
  errandId: string,
  expectedVersion: number,
): Promise<RequesterErrandView> {
  return action<RequesterErrandView>(token, errandId, "cancel", { expectedVersion });
}

export function confirmErrand(
  token: string,
  errandId: string,
  expectedVersion: number,
): Promise<RequesterErrandView> {
  return action<RequesterErrandView>(token, errandId, "confirm", { expectedVersion });
}

export function disputeErrand(
  token: string,
  errandId: string,
  expectedVersion: number,
  reason: string,
): Promise<RequesterErrandView> {
  return action<RequesterErrandView>(token, errandId, "dispute", { expectedVersion, reason });
}

export function withdrawErrand(
  token: string,
  errandId: string,
  expectedVersion: number,
): Promise<CourierErrandView> {
  return action<CourierErrandView>(token, errandId, "withdraw", { expectedVersion });
}

export function pickUpErrand(
  token: string,
  errandId: string,
  expectedVersion: number,
): Promise<CourierErrandView> {
  return action<CourierErrandView>(token, errandId, "pickup", { expectedVersion });
}

export function deliverErrand(
  token: string,
  errandId: string,
  expectedVersion: number,
): Promise<CourierErrandView> {
  return action<CourierErrandView>(token, errandId, "deliver", { expectedVersion });
}

export function reportIssue(
  token: string,
  errandId: string,
  expectedVersion: number,
  reason: string,
): Promise<CourierErrandView> {
  return action<CourierErrandView>(token, errandId, "report-issue", { expectedVersion, reason });
}

export interface PageParams {
  limit?: number;
  cursor?: string;
}

export async function listRequestedErrands(
  token: string,
  params: PageParams = {},
): Promise<RequesterErrandPage> {
  return parse<RequesterErrandPage>(
    await fetch(`${BASE}/me/errands/requested${query({ ...params })}`, {
      headers: headers(token),
    }),
  );
}

export async function listDeliveringErrands(
  token: string,
  params: PageParams = {},
): Promise<CourierErrandPage> {
  return parse<CourierErrandPage>(
    await fetch(`${BASE}/me/errands/delivering${query({ ...params })}`, {
      headers: headers(token),
    }),
  );
}

export async function listErrandHistory(
  token: string,
  params: PageParams = {},
): Promise<HistoryPage> {
  return parse<HistoryPage>(
    await fetch(`${BASE}/me/errands/history${query({ ...params })}`, { headers: headers(token) }),
  );
}

// ---- administration ----------------------------------------------------------

export interface ListReviewsParams extends PageParams {
  reason?: ReviewReason;
}

export async function listReviews(
  token: string,
  params: ListReviewsParams = {},
): Promise<AdminReviewPage> {
  return parse<AdminReviewPage>(
    await fetch(`${BASE}/admin/reviews${query({ ...params })}`, { headers: headers(token) }),
  );
}

export async function resolveReview(
  token: string,
  errandId: string,
  outcome: "COMPLETED" | "REFUNDED",
  reason: string,
  expectedVersion: number,
): Promise<AdminErrandView> {
  return parse<AdminErrandView>(
    await fetch(`${BASE}/admin/reviews/${encodeURIComponent(errandId)}/resolve`, {
      method: "POST",
      headers: headers(token, true),
      body: JSON.stringify({ outcome, reason, expectedVersion }),
    }),
  );
}

export async function getSettings(token: string): Promise<Settings> {
  return parse<Settings>(await fetch(`${BASE}/admin/settings`, { headers: headers(token) }));
}

// putSettings replaces every setting; send the complete object (F6.3.5).
export async function putSettings(token: string, settings: Settings): Promise<Settings> {
  return parse<Settings>(
    await fetch(`${BASE}/admin/settings`, {
      method: "PUT",
      headers: headers(token, true),
      body: JSON.stringify(settings),
    }),
  );
}

export interface ListEventsParams extends PageParams {
  userId?: string;
  errandId?: string;
  type?: ErrandStatus;
  from?: string;
  to?: string;
}

export async function listErrandEvents(
  token: string,
  params: ListEventsParams = {},
): Promise<TransitionPage> {
  return parse<TransitionPage>(
    await fetch(`${BASE}/admin/errand-events${query({ ...params })}`, { headers: headers(token) }),
  );
}

export async function listFlags(token: string): Promise<FlagList> {
  return parse<FlagList>(await fetch(`${BASE}/admin/flags`, { headers: headers(token) }));
}

export async function listFailedOutboxRows(
  token: string,
  params: PageParams = {},
): Promise<OutboxRowPage> {
  return parse<OutboxRowPage>(
    await fetch(`${BASE}/admin/outbox/failed${query({ ...params })}`, { headers: headers(token) }),
  );
}

export async function requeueOutboxRow(
  token: string,
  id: number,
  reason: string,
): Promise<OutboxRow> {
  return parse<OutboxRow>(
    await fetch(`${BASE}/admin/outbox/${id}/requeue`, {
      method: "POST",
      headers: headers(token, true),
      body: JSON.stringify({ reason }),
    }),
  );
}
