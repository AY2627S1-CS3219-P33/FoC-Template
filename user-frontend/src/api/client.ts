import type { AdminUsersPage, AuthConfig, Profile, ProfilePatch } from "./types";

// RequestError carries the service's structured error code so callers can react
// to specific outcomes (e.g. account_not_provisioned) rather than just a status.
export class RequestError extends Error {
  readonly status: number;
  readonly code: string;
  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = "RequestError";
    this.status = status;
    this.code = code;
  }
}

async function parse<T>(response: Response): Promise<T> {
  const text = await response.text();
  const body = text ? JSON.parse(text) : {};
  if (!response.ok) {
    throw new RequestError(
      response.status,
      body.error ?? "request_failed",
      body.message ?? `Request failed with status ${response.status}.`,
    );
  }
  return body as T;
}

function bearer(token: string): HeadersInit {
  return { Authorization: `Bearer ${token}` };
}

export function getConfig(): Promise<AuthConfig> {
  return fetch("/api/auth/config", { cache: "no-store" }).then(parse<AuthConfig>);
}

// provision creates the local account on first sign-in and is idempotent
// afterwards; either way it returns the caller's profile.
export function provision(token: string): Promise<Profile> {
  return fetch("/api/auth/provision", { method: "POST", headers: bearer(token) }).then(
    parse<Profile>,
  );
}

export function getProfile(token: string): Promise<Profile> {
  return fetch("/api/me", { headers: bearer(token) }).then(parse<Profile>);
}

export function updateProfile(token: string, patch: ProfilePatch): Promise<Profile> {
  return fetch("/api/me", {
    method: "PATCH",
    headers: { ...bearer(token), "Content-Type": "application/json" },
    body: JSON.stringify(patch),
  }).then(parse<Profile>);
}

export function logout(token: string): Promise<void> {
  return fetch("/api/auth/logout", { method: "POST", headers: bearer(token) }).then(
    () => undefined,
  );
}

export function listAdminUsers(
  token: string,
  role: "administrator" | "user",
  email: string,
  cursor?: string,
): Promise<AdminUsersPage> {
  const query = new URLSearchParams({ role, per_page: "25" });
  if (email) query.set("email", email);
  if (cursor) query.set("cursor", cursor);
  return fetch(`/api/admin/users?${query}`, { headers: bearer(token) }).then(parse<AdminUsersPage>);
}

export function setAdministrator(token: string, userID: string, enabled: boolean): Promise<void> {
  return fetch(`/api/admin/users/${encodeURIComponent(userID)}/administrator`, {
    method: enabled ? "PUT" : "DELETE",
    headers: bearer(token),
  }).then(async (response) => {
    if (!response.ok) await parse(response);
  });
}
