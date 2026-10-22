import { useCallback, useEffect, useState } from "react";
import { useAuth } from "../../auth/AuthContext";
import { CreditError, getMyWallet, walletTotal, type Wallet } from "../../api/credit";
import type { Profile } from "../../api/types";

const CREDIT_AUDIENCE =
  import.meta.env.VITE_CREDIT_AUDIENCE || "https://api.foc.local/credit-service";

function creditError(err: unknown): string {
  if (err instanceof CreditError) {
    if (err.code === "UNAUTHENTICATED") return "Please sign in again to see your credits.";
    if (err.code === "FORBIDDEN") return "You are not allowed to read this balance.";
    if (err.code === "WALLET_NOT_FOUND")
      return "You do not have a wallet yet. One is created with your first errand.";
    if (err.code === "DEPENDENCY_UNAVAILABLE")
      return "The credit service is temporarily unavailable.";
    return err.message;
  }
  return "Could not reach the credit service.";
}

export function CreditPage() {
  const { profile, getAccessToken } = useAuth();
  const account = profile as Profile;

  const getToken = useCallback(
    () =>
      getAccessToken({
        audience: CREDIT_AUDIENCE,
        scope: "openid profile email credits:read:own",
      }),
    [getAccessToken],
  );

  const [wallet, setWallet] = useState<Wallet | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const token = await getToken();
      setWallet(await getMyWallet(token));
      setError(null);
    } catch (err) {
      setError(creditError(err));
      setWallet(null);
    } finally {
      setLoading(false);
    }
  }, [getToken]);

  useEffect(() => {
    void load();
  }, [load]);

  // The user-service profile also carries a balance. While the credit service is
  // unreachable it is the only figure available, so it is shown as a fallback
  // rather than leaving the page empty.
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
        <button type="button" className="btn outline slim" onClick={() => void load()} disabled={loading}>
          Refresh
        </button>
      </div>

      {error && (
        <div className="banner banner-error" role="alert">
          <span>{error}</span>
        </div>
      )}

      {wallet ? (
        <div className="credit-cards">
          <CreditTile
            label="Available"
            value={wallet.available}
            hint="Ready to commit to a new errand."
            emphasis
          />
          <CreditTile
            label="Reserved"
            value={wallet.reserved}
            hint="Held by errands that have not finished."
          />
          <CreditTile label="Total" value={walletTotal(wallet)} hint="Available plus reserved." />
        </div>
      ) : (
        !loading && (
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

      {loading && !wallet && <p className="muted center mt">Loading balance…</p>}

      <section className="panel mt">
        <h2>How credits move</h2>
        <p className="muted" style={{ marginTop: 0 }}>
          Requesting an errand reserves its reward from your available balance; it stays reserved
          until the errand completes, is cancelled or expires. Delivering an errand transfers its
          reward to you. Every movement belongs to an errand outcome — there is no way to move
          credits directly.
        </p>
      </section>
    </>
  );
}

function CreditTile(props: { label: string; value: string; hint: string; emphasis?: boolean }) {
  return (
    <div className={`credit-card${props.emphasis ? " emphasis" : ""}`}>
      <span className="credit-card-label">{props.label}</span>
      <span className="credit-card-value">{props.value}</span>
      <span className="credit-card-hint">{props.hint}</span>
    </div>
  );
}
