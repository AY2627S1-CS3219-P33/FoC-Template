// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: User-frontend API client for places service data.
// Author review: I validated correctness, edited for style, and added boundary checks.

// Saved places client (order-service /me/places, requester enhancements P3).
// Shapes mirror order-service/openapi/places.yaml. The places are always the
// token subject's own: no call takes a user ID, and another user's place is
// 404 PLACE_NOT_FOUND like a missing one.
//
// Errors are OrderError, the same as every other order-service call.

import { OrderError } from "./order";

const BASE = "/order-service";

export type PlaceKind = "DELIVERY" | "PICKUP";

// The building as supplier-service lists it now; name is empty when it is no
// longer listed at all.
export interface SavedPlaceBuilding {
  id: string;
  name: string;
  retired: boolean;
}

// From the supplier's current version. Times are HH:MM in Asia/Singapore.
export interface SavedSupplierSummary {
  name: string;
  building: string;
  floor: string;
  openingTime: string;
  closingTime: string;
}

interface PlaceCore {
  placeId: string;
  label: string;
  // False once the building is retired or the supplier deleted. A stale place
  // is never removed automatically; the user removes it.
  available: boolean;
  createdAt: string;
}

export interface DeliveryPlace extends PlaceCore {
  kind: "DELIVERY";
  deliveryBuilding: SavedPlaceBuilding;
  deliveryDetail: string;
}

export interface PickupPlace extends PlaceCore {
  kind: "PICKUP";
  supplierId: string;
  // Present only while available.
  supplier?: SavedSupplierSummary;
}

export type SavedPlace = DeliveryPlace | PickupPlace;

export interface PlaceList {
  items: SavedPlace[];
}

export type PlaceCreate =
  | { kind: "DELIVERY"; label: string; deliveryBuildingId: string; deliveryDetail: string }
  | { kind: "PICKUP"; label: string; supplierId: string };

// The kind, supplier and building cannot change; deliveryDetail is only for a
// DELIVERY place.
export interface PlacePatch {
  label?: string;
  deliveryDetail?: string;
}

// Same as order.ts: a non-2xx body is the common Error shape.
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

// Newest first. Not paged: a user has at most 20 places of each kind.
export async function listPlaces(token: string, kind?: PlaceKind): Promise<PlaceList> {
  const qs = kind ? `?kind=${kind}` : "";
  return parse<PlaceList>(await fetch(`${BASE}/me/places${qs}`, { headers: headers(token) }));
}

export async function createPlace(token: string, body: PlaceCreate): Promise<SavedPlace> {
  return parse<SavedPlace>(
    await fetch(`${BASE}/me/places`, {
      method: "POST",
      headers: headers(token, true),
      body: JSON.stringify(body),
    }),
  );
}

export async function updatePlace(
  token: string,
  placeId: string,
  patch: PlacePatch,
): Promise<SavedPlace> {
  return parse<SavedPlace>(
    await fetch(`${BASE}/me/places/${encodeURIComponent(placeId)}`, {
      method: "PATCH",
      headers: headers(token, true),
      body: JSON.stringify(patch),
    }),
  );
}

export async function deletePlace(token: string, placeId: string): Promise<void> {
  const response = await fetch(`${BASE}/me/places/${encodeURIComponent(placeId)}`, {
    method: "DELETE",
    headers: headers(token),
  });
  if (!response.ok) await parse<never>(response);
}
