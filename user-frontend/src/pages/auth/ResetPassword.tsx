import { useState, type FormEvent } from "react";

// Matches wireframe image15 (reset password + "check your inbox" state).
// Production password reset is delegated to Auth0; this screen reproduces the
// flow and the deliberately non-revealing confirmation message (NFR3.1.6).
export function ResetPassword({ onBack }: { onBack: () => void }) {
  const [email, setEmail] = useState("");
  const [sent, setSent] = useState(false);

  function onSubmit(event: FormEvent) {
    event.preventDefault();
    if (!email.trim()) return;
    setSent(true);
  }

  if (sent) {
    return (
      <div className="auth-card padded">
        <div className="inbox">
          <div className="mail">✉</div>
          <h1 style={{ fontSize: "1.3rem" }}>Check your inbox</h1>
          <p className="lead" style={{ marginTop: "0.8rem" }}>
            If an account exists for that email, a reset link is on its way. The
            link expires in 30 minutes.
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
        <button type="submit" className="btn">
          Send reset link
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
