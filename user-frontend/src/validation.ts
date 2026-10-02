// Client-side mirror of the service's profile validation (internal/service).
// The server remains the source of truth; these checks give immediate feedback.

const DISPLAY_NAME_MAX = 100;
const MOBILE_MAX = 32;

// Matches ASCII and Unicode control characters, which the service rejects.
// eslint-disable-next-line no-control-regex
const CONTROL_CHARS = /[\u0000-\u001F\u007F-\u009F]/;

export function validateDisplayName(value: string): string | null {
  if (value.length > DISPLAY_NAME_MAX) {
    return `Display name must be at most ${DISPLAY_NAME_MAX} characters.`;
  }
  if (CONTROL_CHARS.test(value)) {
    return "Display name must not contain control characters.";
  }
  return null;
}

export function validateMobile(value: string): string | null {
  if (value.length > MOBILE_MAX) {
    return `Mobile number must be at most ${MOBILE_MAX} characters.`;
  }
  if (CONTROL_CHARS.test(value)) {
    return "Mobile number must not contain control characters.";
  }
  return null;
}

export { DISPLAY_NAME_MAX, MOBILE_MAX };

// Password policy from F1.1.2. The wireframe labels this "7+ characters"; the
// functional requirement specifies at least eight, so eight is enforced here.
export interface PasswordChecks {
  length: boolean;
  uppercase: boolean;
  lowercase: boolean;
  number: boolean;
  symbol: boolean;
}

export function checkPassword(value: string): PasswordChecks {
  return {
    length: value.length >= 8,
    uppercase: /[A-Z]/.test(value),
    lowercase: /[a-z]/.test(value),
    number: /[0-9]/.test(value),
    symbol: /[^A-Za-z0-9\s]/.test(value),
  };
}

export function passwordSatisfied(checks: PasswordChecks): boolean {
  return Object.values(checks).every(Boolean);
}

// Only the exact NUS student domain is accepted for account creation (F1.1.5;
// the service enforces u.nus.edu).
export function isNusEmail(email: string): boolean {
  return /^[^@\s]+@u\.nus\.edu$/.test(email.trim().toLowerCase());
}
