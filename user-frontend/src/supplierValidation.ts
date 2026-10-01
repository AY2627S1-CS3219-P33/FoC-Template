// Client-side mirror of the Supplier Service field contract
// (supplier-service/openapi/common.yaml SupplierFields). The service remains the
// source of truth; these checks give immediate feedback and match its limits.

import type { SupplierFields } from "./api/supplier";

export const LIMITS = {
  name: 120,
  type: 64,
  building: 120,
  floor: 32,
  locationDescription: 500,
  imageUrl: 2048,
} as const;

const TIME_RE = /^(?:[01][0-9]|2[0-3]):[0-5][0-9]$/;
const URL_RE = /^https?:\/\//;

export type SupplierFormErrors = Partial<Record<keyof SupplierFields, string>>;

function requiredText(value: string, label: string, max: number): string | null {
  const trimmed = value.trim();
  if (trimmed.length === 0) return `${label} is required.`;
  if (value.length > max) return `${label} must be at most ${max} characters.`;
  return null;
}

function coordinate(value: string, label: string, min: number, max: number): string | null {
  if (value.trim() === "") return `${label} is required.`;
  const num = Number(value);
  if (!Number.isFinite(num)) return `${label} must be a number.`;
  if (num < min || num > max) return `${label} must be between ${min} and ${max}.`;
  return null;
}

// Validates the string-based form draft. Coordinates and times arrive as strings
// from the inputs; this returns one message per invalid field.
export function validateSupplierForm(draft: {
  name: string;
  type: string;
  building: string;
  floor: string;
  locationDescription: string;
  latitude: string;
  longitude: string;
  openingTime: string;
  closingTime: string;
  imageUrl: string;
}): SupplierFormErrors {
  const errors: SupplierFormErrors = {};

  const name = requiredText(draft.name, "Name", LIMITS.name);
  if (name) errors.name = name;
  const type = requiredText(draft.type, "Type", LIMITS.type);
  if (type) errors.type = type;
  const building = requiredText(draft.building, "Building", LIMITS.building);
  if (building) errors.building = building;
  const floor = requiredText(draft.floor, "Floor", LIMITS.floor);
  if (floor) errors.floor = floor;
  const location = requiredText(
    draft.locationDescription,
    "Location description",
    LIMITS.locationDescription,
  );
  if (location) errors.locationDescription = location;

  const lat = coordinate(draft.latitude, "Latitude", -90, 90);
  if (lat) errors.latitude = lat;
  const lon = coordinate(draft.longitude, "Longitude", -180, 180);
  if (lon) errors.longitude = lon;

  if (!TIME_RE.test(draft.openingTime)) errors.openingTime = "Use 24-hour HH:MM.";
  if (!TIME_RE.test(draft.closingTime)) errors.closingTime = "Use 24-hour HH:MM.";

  const image = draft.imageUrl.trim();
  if (image !== "") {
    if (!URL_RE.test(image)) errors.imageUrl = "Must start with http:// or https://.";
    else if (image.length > LIMITS.imageUrl)
      errors.imageUrl = `Must be at most ${LIMITS.imageUrl} characters.`;
  }

  return errors;
}

export function hasErrors(errors: SupplierFormErrors): boolean {
  return Object.keys(errors).length > 0;
}
