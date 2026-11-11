// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: User-frontend API client for types service data.
// Author review: I validated correctness, edited for style, and added boundary checks.

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
}

export interface ProfilePatch {
  display_name?: string;
  mobile_number?: string;
}

export interface AdminUser {
  user_id: string;
  email: string;
  name?: string;
}

export interface AdminUsersPage {
  users: AdminUser[];
  total?: number;
  page: number;
  per_page: number;
  role?: "administrator" | "user";
  next_cursor?: string;
}
