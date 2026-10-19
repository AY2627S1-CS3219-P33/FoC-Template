import { useCallback, useEffect, useState } from "react";
import { useAuth } from "../../auth/AuthContext";
import {
  CreditError,
  getMyBalance,
  listMyReservations,
  listMyTransactions,
  type Balance,
  type Reservation,
  type Transaction,
  type TransactionKind,
} from "../../api/credit";
import type { Profile } from "../../api/types";
import { usePagedList } from "../errands/usePagedList";
import { creditText, formatDateTime } from "../errands/format";

const CREDIT_AUDIENCE =
  import.meta.env.VITE_CREDIT_AUDIENCE || "https://api.foc.local/credit-service";

const PAGE_SIZE = 25;

type Tab = "ledger" | "holds";

const KIND_LABELS: Record<TransactionKind, string> = {
  RESERVE: "Reserved for an errand",
  RELEASE: "Reservation returned",
  ADJUST: "Reward adjusted",
  TRANSFER_OUT: "Paid to a courier",
  TRANSFER_IN: "Earned as courier",
};

function creditError(err: unknown): string {
  if (err instanceof CreditError) {
    if (err.code === "UNAUTHENTICATED") return "Please sign in again to see your credits.";
    if (err.code === "FORBIDDEN") return "You are not allowed to read this balance.";
    if (err.code === "ACCOUNT_NOT_FOUND")
      return "You do not have a credit account yet. One is created with your first errand.";
    if (err.code === "DEPENDENCY_UNAVAILABLE")
      return "The credit service is temporarily unavailable.";
    return err.message;
  }
  return "Could not reach the credit service.";
}

export function CreditPage() {
  const { profile, getAccessToken } = useAuth();
  const account = profile as Profile;
  const [tab, setTab] = useState<Tab>("ledger");

  const getToken = useCallback(
    () =>
      getAccessToken({
        audience: CREDIT_AUDIENCE,
        scope: "openid profile email credits:read",
      }),
    [getAccessToken],
  );

  const [balance, setBalance] = useState<Balance | null>(null);
  const [balanceError, setBalanceError] = useState<string | null>(null);
  const [loadingBalance, setLoadingBalance] = useState(true);

  const loadBalance = useCallback(async () => {
    setLoadingBalance(true);
    try {
      const token = await getToken();
      setBalance(await getMyBalance(token));
      setBalanceError(null);
    } catch (err) {
      setBalanceError(creditError(err));
      setBalance(null);
    } finally {
      setLoadingBalance(false);
    }
  }, [getToken]);

  useEffect(() => {
    void loadBalance();
  }, [loadBalance]);

  const loadLedger = useCallback(
    async (cursor?: string) => {
      const token = await getToken();
      return listMyTransactions(token, { limit: PAGE_SIZE, cursor });
    },
    [getToken],
  );

  const loadHolds = useCallback(
    async (cursor?: string) => {
      const token = await getToken();
      return listMyReservations(token, { limit: PAGE_SIZE, cursor });
    },
    [getToken],
  );

  const ledger = usePagedList<Transaction>(loadLedger, creditError);
  const holds = usePagedList<Reservation>(loadHolds, creditError);

  // The profile carries a balance from user-service too. While credit-service
  // is unreachable it is the only figure available, so it is shown as a
  // fallback rather than leaving the page empty.
  const fallback = account.credit_balance;

  return (
    <>
      <div className="page-head">
        <div>
          <h1>Credits</h1>
          <p className="muted" style={{ margin: "0.3rem 0 0" }}>
            Credits only move between accounts on this platform. They cannot be bought, cashed out
            or transferred outside an errand.
          </p>
        </div>
        <button
          type="button"
          className="btn outline slim"
          onClick={() => {
            void loadBalance();
            ledger.reload();
            holds.reload();
          }}
          disabled={loadingBalance}
        >
          Refresh
        </button>
      </div>

      {balanceError && (
        <div className="banner banner-error" role="alert">
          <span>{balanceError}</span>
        </div>
      )}

      {balance ? (
        <div className="credit-cards">
          <CreditTile
            label="Available"
            value={String(balance.available)}
            hint="Ready to commit to a new errand."
            emphasis
          />
          <CreditTile
            label="Reserved"
            value={String(balance.reserved)}
            hint="Held by errands that have not finished."
          />
          <CreditTile label="Total" value={String(balance.total)} hint="Available plus reserved." />
        </div>
      ) : (
        !loadingBalance && (
          <div className="credit-cards">
            <CreditTile
              label="Balance"
              value={fallback === null ? "—" : String(fallback)}
              hint={
                fallback === null
                  ? "No balance is available from either service."
                  : "From your profile, because the credit service could not be reached."
              }
              emphasis
            />
          </div>
        )
      )}

      {loadingBalance && !balance && <p className="muted center mt">Loading balance…</p>}

      <div className="tabs" role="tablist">
        <button
          type="button"
          role="tab"
          className="tab"
          aria-selected={tab === "ledger"}
          onClick={() => setTab("ledger")}
        >
          History
        </button>
        <button
          type="button"
          role="tab"
          className="tab"
          aria-selected={tab === "holds"}
          onClick={() => setTab("holds")}
        >
          Active holds
        </button>
      </div>

      <div className="tab-body">
        {tab === "ledger" ? (
          <>
            {ledger.error && (
              <div className="banner banner-error" role="alert">
                <span>{ledger.error}</span>
              </div>
            )}

            {!ledger.error && !ledger.loading && ledger.items.length === 0 && (
              <div className="placeholder">
                <p>No credit movements yet.</p>
                <p className="muted">
                  Requesting an errand reserves credits; delivering one earns them.
                </p>
              </div>
            )}

            {ledger.items.length > 0 && (
              <ul className="ledger">
                {ledger.items.map((entry) => (
                  <li key={entry.id}>
                    <div className="ledger-main">
                      <span className="ledger-label">
                        {KIND_LABELS[entry.kind] ?? entry.kind}
                      </span>
                      {entry.description && (
                        <span className="muted small">{entry.description}</span>
                      )}
                      <span className="muted small">{formatDateTime(entry.createdAt)}</span>
                    </div>
                    <div className="ledger-amounts">
                      <span className={entry.amount < 0 ? "amount out" : "amount in"}>
                        {entry.amount > 0 ? "+" : ""}
                        {entry.amount}
                      </span>
                      <span className="muted small">balance {entry.balanceAfter}</span>
                    </div>
                  </li>
                ))}
              </ul>
            )}

            {ledger.loading && <p className="muted center mt">Loading…</p>}
            {ledger.hasMore && !ledger.loading && (
              <div className="center mt">
                <button type="button" className="btn outline slim" onClick={ledger.loadMore}>
                  Load more
                </button>
              </div>
            )}
          </>
        ) : (
          <>
            {holds.error && (
              <div className="banner banner-error" role="alert">
                <span>{holds.error}</span>
              </div>
            )}

            {!holds.error && !holds.loading && holds.items.length === 0 && (
              <div className="placeholder">
                <p>Nothing is being held.</p>
                <p className="muted">
                  An errand holds its reward from when you request it until it completes, is
                  cancelled or expires.
                </p>
              </div>
            )}

            {holds.items.length > 0 && (
              <ul className="ledger">
                {holds.items.map((hold) => (
                  <li key={hold.errandId}>
                    <div className="ledger-main">
                      <span className="ledger-label">{creditText(hold.amount)} held</span>
                      <span className="admin-id">{hold.errandId}</span>
                      <span className="muted small">since {formatDateTime(hold.createdAt)}</span>
                    </div>
                  </li>
                ))}
              </ul>
            )}

            {holds.loading && <p className="muted center mt">Loading…</p>}
            {holds.hasMore && !holds.loading && (
              <div className="center mt">
                <button type="button" className="btn outline slim" onClick={holds.loadMore}>
                  Load more
                </button>
              </div>
            )}
          </>
        )}
      </div>

      <p className="muted small mt">
        The credit service has no implementation yet. These screens follow the proposed contract in
        <code className="mono"> credit-service/openapi.yaml</code>, so they will show an error until
        that service is running.
      </p>
    </>
  );
}

function CreditTile(props: {
  label: string;
  value: string;
  hint: string;
  emphasis?: boolean;
}) {
  return (
    <div className={`credit-card${props.emphasis ? " emphasis" : ""}`}>
      <span className="credit-card-label">{props.label}</span>
      <span className="credit-card-value">{props.value}</span>
      <span className="credit-card-hint">{props.hint}</span>
    </div>
  );
}
