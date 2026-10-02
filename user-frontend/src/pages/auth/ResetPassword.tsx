import { useRef, useState, type FormEvent } from "react";
import { useAuth } from "../../auth/AuthContext";

// Matches wireframe image15 (reset password + "check your inbox" state).
// Request a real Auth0 password-reset email without revealing account existence.
export function ResetPassword({ onBack }: { onBack: () => void }) {
  const { resetPassword } = useAuth();
  const [email, setEmail] = useState("");
  const [sent, setSent] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const submitting = useRef(false);

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    if (!email.trim() || submitting.current) return;
    submitting.current = true;
    setBusy(true);
    setError(null);
    try {
      await resetPassword(email);
      setSent(true);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not request a reset email.");
    } finally {
      submitting.current = false;
      setBusy(false);
    }
  }

  if (sent) {
    return (
      <div className="auth-card padded">
        <div className="inbox">
          <div className="mail">✉</div>
          <h1 style={{ fontSize: "1.3rem" }}>Check your inbox</h1>
          <p className="lead" style={{ marginTop: "0.8rem" }}>
            If an account exists for that email, a reset link is on its way. The
            link's expiry is configured by Auth0.
          </p>
          <p className="muted" style={{ fontSize: "0.9rem" }}>
            Didn&apos;t get it?{" "}
            <button type="button" className="link-btn" onClick={() => setSent(false)}>
              Resend
            </button>
          </p>
          <button type="button" className="btn outline mt" onClick={onBack}>
            ← Back to log in
          </button>
        </div>
      </div>
    );
  }

  return (
    <div className="auth-card padded">
      <h1 style={{ fontSize: "1.5rem" }}>Reset your password</h1>
      <p className="lead" style={{ marginTop: "0.6rem" }}>
        Enter the email on your account and we&apos;ll send a reset link.
      </p>
      <form onSubmit={onSubmit}>
        {error && <div className="banner banner-error" role="alert">{error}</div>}
        <div className="field">
          <label htmlFor="reset-email">Email</label>
          <input
            id="reset-email"
            className="input"
            type="email"
            autoComplete="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            required
          />
        </div>
        <button type="submit" className="btn" disabled={busy}>
          {busy ? "Sending…" : "Send reset link"}
        </button>
      </form>
      <p className="center mt">
        <button type="button" className="link-btn" onClick={onBack}>
          ← Back to log in
        </button>
      </p>
    </div>
  );
}
