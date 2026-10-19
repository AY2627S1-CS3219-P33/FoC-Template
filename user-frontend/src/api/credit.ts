// Credit Service client.
//
// PROVISIONAL. credit-service has no implementation yet; this mirrors the
// proposed read-only contract in credit-service/openapi.yaml, which was
// derived from the credit.* message contract in order-service/docs/contracts.md.
// It is the only module that depends on that proposal: if the credit owner
// settles on different shapes, this file and credit-service/openapi.yaml are
// what change.
//
// There are no write calls on purpose. The credit economy is closed and credits
// move only through order-service's credit.reserve / adjust / release /
// transfer commands, never through a client request.

const BASE = "/credit-service";

export type CreditErrorCode =
  | "INVALID_ARGUMENT"
  | "UNAUTHENTICATED"
  | "FORBIDDEN"
  | "ACCOUNT_NOT_FOUND"
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

export interface Balance {
  userId: string;
  // Committable to a new errand.
  available: number;
  // Held by errands awaiting an outcome.
  reserved: number;
  total: number;
  updatedAt: string;
}

export type TransactionKind = "RESERVE" | "RELEASE" | "ADJUST" | "TRANSFER_OUT" | "TRANSFER_IN";

export interface Transaction {
  id: string;
  kind: TransactionKind;
  // Signed effect on the available balance.
  amount: number;
  balanceAfter: number;
  errandId?: string;
  description?: string;
  createdAt: string;
}

export interface TransactionPage {
  items: Transaction[];
  nextCursor?: string;
}

export interface Reservation {
  errandId: string;
  amount: number;
  createdAt: string;
}

export interface ReservationPage {
  items: Reservation[];
  nextCursor?: string;
}

// ---- HTTP implementation -----------------------------------------------------

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

function query(params: Record<string, string | number | undefined>): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== "") search.set(key, String(value));
  }
  const qs = search.toString();
  return qs ? `?${qs}` : "";
}

export interface PageParams {
  limit?: number;
  cursor?: string;
}

export async function getMyBalance(token: string): Promise<Balance> {
  return parse<Balance>(await fetch(`${BASE}/credits/me`, { headers: headers(token) }));
}

export async function listMyTransactions(
  token: string,
  params: PageParams = {},
): Promise<TransactionPage> {
  return parse<TransactionPage>(
    await fetch(`${BASE}/credits/me/transactions${query({ ...params })}`, {
      headers: headers(token),
    }),
  );
}

export async function listMyReservations(
  token: string,
  params: PageParams = {},
): Promise<ReservationPage> {
  return parse<ReservationPage>(
    await fetch(`${BASE}/credits/me/reservations${query({ ...params })}`, {
      headers: headers(token),
    }),
  );
}

export async function getAccountBalance(token: string, userId: string): Promise<Balance> {
  return parse<Balance>(
    await fetch(`${BASE}/admin/credits/${encodeURIComponent(userId)}`, {
      headers: headers(token),
    }),
  );
}
