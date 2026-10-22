// Credit Service client.
//
// Mirrors the frozen read surface in credit-service/openapi.yaml (v1.0.0). The
// only user-facing route is GET /v1/wallet/me: wallet creation and the internal
// per-owner routes are service-to-service (machine tokens) and reserve / adjust
// / release / transfer are RabbitMQ commands, never client HTTP calls. Credits
// are whole numbers serialised as strings, so they are kept as strings here and
// summed with BigInt to stay exact for large balances.

const BASE = "/credit-service";

export type CreditErrorCode =
  | "INVALID_ARGUMENT"
  | "UNAUTHENTICATED"
  | "FORBIDDEN"
  | "WALLET_NOT_FOUND"
  | "WALLET_CONFLICT"
  | "DEPENDENCY_UNAVAILABLE"
  | "INTERNAL"
  | "NETWORK";

export class CreditError extends Error {
  readonly status: number;
  readonly code: CreditErrorCode;
  constructor(status: number, code: CreditErrorCode, message: string) {
    super(message);
    this.name = "CreditError";
    this.status = status;
    this.code = code;
  }
}

export interface Wallet {
  ownerId: string;
  // Whole-number credits as strings: available can be spent, reserved is held
  // for open errands.
  available: string;
  reserved: string;
  createdAt: string;
}

// walletTotal derives available + reserved exactly. The frozen contract does not
// return a total, so it is computed for display.
export function walletTotal(wallet: Wallet): string {
  try {
    return (BigInt(wallet.available) + BigInt(wallet.reserved)).toString();
  } catch {
    return "—";
  }
}

async function parse<T>(response: Response): Promise<T> {
  const text = await response.text();
  const body = text ? JSON.parse(text) : {};
  if (!response.ok) {
    throw new CreditError(
      response.status,
      body.code ?? "INTERNAL",
      body.message ?? `Request failed with status ${response.status}.`,
    );
  }
  return body as T;
}

function headers(token: string): Record<string, string> {
  return { Authorization: `Bearer ${token}` };
}

// getMyWallet reads the caller's wallet, found by the Auth0 subject of the
// token; there is no way to name another user's wallet.
export async function getMyWallet(token: string): Promise<Wallet> {
  return parse<Wallet>(await fetch(`${BASE}/v1/wallet/me`, { headers: headers(token) }));
}
