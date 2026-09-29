// Shapes mirror the user-service HTTP contract (internal/service.Profile and the
// /api/auth/config response). Keep these in sync with the Go service.

export type Role = "USER" | "ADMIN" | "SUPER_ADMIN";

export interface Profile {
  id: string;
  username: string;
  email: string;
  role: Role;
  active: boolean;
  display_name: string;
  mobile_number: string;
  // null means the credit adapter is missing or failed; it never means zero.
  credit_balance: number | null;
}

export interface AuthConfig {
  domain: string;
  clientId: string;
  audience: string;
  // dev is true only when the service runs with the in-process mock issuer.
  dev?: boolean;
}

export interface ProfilePatch {
  display_name?: string;
  mobile_number?: string;
}
