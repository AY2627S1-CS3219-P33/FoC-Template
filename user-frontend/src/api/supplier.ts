// Supplier Service client. Shapes mirror the frozen OpenAPI contract
// (supplier-service/openapi). The service is reached at /supplier-service (its
// documented gateway route); in dev the Vite proxy forwards there.
//
// The Vite development proxy forwards this path to the supplier service.

const BASE = "/supplier-service";

export type SupplierErrorCode =
  | "INVALID_ARGUMENT"
  | "UNAUTHENTICATED"
  | "FORBIDDEN"
  | "SUPPLIER_NOT_FOUND"
  | "SUPPLIER_VERSION_NOT_FOUND"
  | "SUPPLIER_NAME_CONFLICT"
  | "SUPPLIER_HAS_ACTIVE_ERRANDS"
  | "DELETION_FENCE_UNAVAILABLE"
  | "DEPENDENCY_UNAVAILABLE"
  | "INTERNAL"
  | "NETWORK";

export interface FieldError {
  field: string;
  message: string;
}

export class SupplierError extends Error {
  readonly status: number;
  readonly code: SupplierErrorCode;
  readonly fields: FieldError[];
  constructor(status: number, code: SupplierErrorCode, message: string, fields: FieldError[] = []) {
    super(message);
    this.name = "SupplierError";
    this.status = status;
    this.code = code;
    this.fields = fields;
  }
}

export interface SupplierFields {
  name: string;
  type: string;
  building: string;
  floor: string;
  locationDescription: string;
  latitude: number;
  longitude: number;
  openingTime: string; // HH:MM
  closingTime: string; // HH:MM
  imageUrl: string | null;
}

export interface Supplier extends SupplierFields {
  supplierId: string;
  versionId: string;
  available: boolean;
  createdAt: string;
  updatedAt: string;
}

export interface SupplierPage {
  items: Supplier[];
  nextCursor?: string;
}

export type SupplierWrite = SupplierFields;
export type SupplierPatch = Partial<SupplierFields>;

export interface ListParams {
  q?: string;
  type?: string;
  limit?: number;
  cursor?: string;
}

// ---- HTTP implementation -----------------------------------------------------

async function parse<T>(response: Response): Promise<T> {
  const text = await response.text();
  const body = text ? JSON.parse(text) : {};
  if (!response.ok) {
    throw new SupplierError(
      response.status,
      body.code ?? "INTERNAL",
      body.message ?? `Request failed with status ${response.status}.`,
      body.fields ?? [],
    );
  }
  return body as T;
}

function headers(token: string, json = false): HeadersInit {
  const h: Record<string, string> = { Authorization: `Bearer ${token}` };
  if (json) h["Content-Type"] = "application/json";
  return h;
}

async function httpList(token: string, params: ListParams): Promise<SupplierPage> {
  const query = new URLSearchParams();
  if (params.q) query.set("q", params.q);
  if (params.type) query.set("type", params.type);
  if (params.limit) query.set("limit", String(params.limit));
  if (params.cursor) query.set("cursor", params.cursor);
  const qs = query.toString();
  return parse<SupplierPage>(
    await fetch(`${BASE}/suppliers${qs ? `?${qs}` : ""}`, { headers: headers(token) }),
  );
}

async function httpGet(token: string, id: string): Promise<Supplier> {
  return parse<Supplier>(
    await fetch(`${BASE}/suppliers/${encodeURIComponent(id)}`, { headers: headers(token) }),
  );
}

async function httpCreate(token: string, body: SupplierWrite): Promise<Supplier> {
  return parse<Supplier>(
    await fetch(`${BASE}/suppliers`, {
      method: "POST",
      headers: headers(token, true),
      body: JSON.stringify(body),
    }),
  );
}

async function httpUpdate(token: string, id: string, patch: SupplierPatch): Promise<Supplier> {
  return parse<Supplier>(
    await fetch(`${BASE}/suppliers/${encodeURIComponent(id)}`, {
      method: "PATCH",
      headers: headers(token, true),
      body: JSON.stringify(patch),
    }),
  );
}

async function httpDelete(token: string, id: string): Promise<void> {
  const response = await fetch(`${BASE}/suppliers/${encodeURIComponent(id)}`, {
    method: "DELETE",
    // Idempotency-Key identifies one logical deletion; reuse it when retrying.
    headers: { ...headers(token), "Idempotency-Key": crypto.randomUUID() },
  });
  if (response.status === 204 || response.status === 202) return;
  await parse<unknown>(response);
}

// ---- public API ---------------------------------------------------------------

export function listSuppliers(token: string, params: ListParams): Promise<SupplierPage> {
  return httpList(token, params);
}
export function getSupplier(token: string, id: string): Promise<Supplier> {
  return httpGet(token, id);
}
export function createSupplier(token: string, body: SupplierWrite): Promise<Supplier> {
  return httpCreate(token, body);
}
export function updateSupplier(token: string, id: string, patch: SupplierPatch): Promise<Supplier> {
  return httpUpdate(token, id, patch);
}
export function deleteSupplier(token: string, id: string): Promise<void> {
  return httpDelete(token, id);
}
