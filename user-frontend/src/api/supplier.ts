// Supplier Service client. Shapes mirror the frozen OpenAPI contract
// (supplier-service/openapi). The service is reached at /supplier-service (its
// documented gateway route); in dev the Vite proxy forwards there.
//
// Searching, filtering and sorting are the service's job: q, type, building and
// sort are query parameters, and a cursor is bound to the filters and sort that
// produced it. The client never re-sorts or re-filters a page.

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
  | "BUILDING_NOT_FOUND"
  | "BUILDING_CONFLICT"
  | "BUILDING_IN_USE"
  | "SUPPLIER_TYPE_NOT_FOUND"
  | "SUPPLIER_TYPE_CONFLICT"
  | "SUPPLIER_TYPE_IN_USE"
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
  // building and type are the canonical names of managed lookup rows, not free
  // text: they must match an existing, non-retired building or supplier type.
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
  // The lookup rows building and type resolve to. Lookup names never change, so
  // a historical version keeps the name it was created with.
  buildingId: string;
  typeId: string;
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

// Ascending by name (default), type or building; the "-" prefix reverses every
// key. Any other value is rejected as an invalid sort field.
export type SupplierSort = "name" | "-name" | "type" | "-type" | "building" | "-building";

export interface ListParams {
  q?: string;
  type?: string;
  building?: string;
  sort?: SupplierSort;
  limit?: number;
  cursor?: string;
}

// ---- lookups -----------------------------------------------------------------

export interface Lookup {
  id: string;
  // Canonical display name. Immutable: there is no rename.
  name: string;
  retired: boolean;
}

export interface LookupList {
  items: Lookup[];
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

function headers(token: string, json = false): Record<string, string> {
  const h: Record<string, string> = { Authorization: `Bearer ${token}` };
  if (json) h["Content-Type"] = "application/json";
  return h;
}

function query(params: Record<string, string | number | boolean | undefined>): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== "") search.set(key, String(value));
  }
  const qs = search.toString();
  return qs ? `?${qs}` : "";
}

// ---- suppliers ---------------------------------------------------------------

export async function listSuppliers(token: string, params: ListParams): Promise<SupplierPage> {
  return parse<SupplierPage>(
    await fetch(`${BASE}/suppliers${query({ ...params })}`, { headers: headers(token) }),
  );
}

export async function getSupplier(token: string, id: string): Promise<Supplier> {
  return parse<Supplier>(
    await fetch(`${BASE}/suppliers/${encodeURIComponent(id)}`, { headers: headers(token) }),
  );
}

export async function createSupplier(token: string, body: SupplierWrite): Promise<Supplier> {
  return parse<Supplier>(
    await fetch(`${BASE}/suppliers`, {
      method: "POST",
      headers: headers(token, true),
      body: JSON.stringify(body),
    }),
  );
}

export async function updateSupplier(
  token: string,
  id: string,
  patch: SupplierPatch,
): Promise<Supplier> {
  return parse<Supplier>(
    await fetch(`${BASE}/suppliers/${encodeURIComponent(id)}`, {
      method: "PATCH",
      headers: headers(token, true),
      body: JSON.stringify(patch),
    }),
  );
}

export async function deleteSupplier(token: string, id: string): Promise<void> {
  const response = await fetch(`${BASE}/suppliers/${encodeURIComponent(id)}`, {
    method: "DELETE",
    // Idempotency-Key identifies one logical deletion; reuse it when retrying.
    headers: { ...headers(token), "Idempotency-Key": crypto.randomUUID() },
  });
  if (response.status === 204 || response.status === 202) return;
  await parse<unknown>(response);
}

// ---- buildings ---------------------------------------------------------------

// Unpaginated and capped at 500 rows, ordered by lookup key. includeRetired
// adds retired rows, which cannot be assigned but still resolve for existing
// versions.
export async function listBuildings(token: string, includeRetired = false): Promise<LookupList> {
  return parse<LookupList>(
    await fetch(`${BASE}/buildings${query({ includeRetired: includeRetired || undefined })}`, {
      headers: headers(token),
    }),
  );
}

export async function createBuilding(token: string, name: string): Promise<Lookup> {
  return parse<Lookup>(
    await fetch(`${BASE}/buildings`, {
      method: "POST",
      headers: headers(token, true),
      body: JSON.stringify({ name }),
    }),
  );
}

// Retiring an already retired building succeeds without change.
export async function retireBuilding(token: string, id: string): Promise<void> {
  const response = await fetch(`${BASE}/buildings/${encodeURIComponent(id)}`, {
    method: "DELETE",
    headers: headers(token),
  });
  if (response.status === 204) return;
  await parse<unknown>(response);
}

// ---- supplier types ----------------------------------------------------------

export async function listSupplierTypes(
  token: string,
  includeRetired = false,
): Promise<LookupList> {
  return parse<LookupList>(
    await fetch(`${BASE}/supplier-types${query({ includeRetired: includeRetired || undefined })}`, {
      headers: headers(token),
    }),
  );
}

export async function createSupplierType(token: string, name: string): Promise<Lookup> {
  return parse<Lookup>(
    await fetch(`${BASE}/supplier-types`, {
      method: "POST",
      headers: headers(token, true),
      body: JSON.stringify({ name }),
    }),
  );
}

export async function retireSupplierType(token: string, id: string): Promise<void> {
  const response = await fetch(`${BASE}/supplier-types/${encodeURIComponent(id)}`, {
    method: "DELETE",
    headers: headers(token),
  });
  if (response.status === 204) return;
  await parse<unknown>(response);
}
