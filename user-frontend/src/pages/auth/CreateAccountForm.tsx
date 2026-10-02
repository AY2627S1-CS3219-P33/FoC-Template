import { useMemo, useRef, useState, type FormEvent } from "react";
import { useAuth } from "../../auth/AuthContext";
import { checkPassword, isNusEmail, passwordSatisfied } from "../../validation";

const CHECK_LABELS: Record<string, string> = {
  length: "8+ characters",
  uppercase: "uppercase",
  lowercase: "lowercase",
  number: "number",
  symbol: "symbol",
};

// Matches wireframe image16 (create account).
export function CreateAccountForm({ dev, onLogin }: { dev: boolean; onLogin: (email: string) => void }) {
  const { signUpDev, signUpAuth0, hostedLogin } = useAuth();
  const [username, setUsername] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [agree, setAgree] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [created, setCreated] = useState(false);
  const submitting = useRef(false);

  const checks = useMemo(() => checkPassword(password), [password]);
  const pwOk = passwordSatisfied(checks);
  const emailOk = email === "" || isNusEmail(email);
  const confirmOk = confirm === "" || confirm === password;

  const canSubmit =
    username.trim().length > 0 &&
    isNusEmail(email) &&
    pwOk &&
    confirm === password &&
    agree &&
    !busy;

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    if (!canSubmit || submitting.current) return;
    submitting.current = true;
    setError(null);
    setBusy(true);
    try {
      if (dev) {
        await signUpDev(username, email, false);
      } else {
        await signUpAuth0(username, email, password);
        setCreated(true);
        setPassword("");
        setConfirm("");
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not create account.");
    } finally {
      submitting.current = false;
      setBusy(false);
    }
  }

  if (created) {
    return (
      <div role="status">
        <h2>Account created</h2>
        <p>Check your email for the verification link. Verify your email before signing in to use your account.</p>
        <button type="button" className="btn" onClick={() => onLogin(email.trim())}>Go to log in</button>
      </div>
    );
  }

  return (
    <form onSubmit={onSubmit}>
      {error && (
        <div className="banner banner-error" role="alert">
          <span className="icon">⚠</span>
          <span>{error}</span>
        </div>
      )}

      <div className="field">
        <label htmlFor="username">Username</label>
        <input
          id="username"
          className="input"
          type="text"
          autoComplete="username"
          value={username}
          onChange={(e) => setUsername(e.target.value)}
          required
        />
      </div>

      <div className="field">
        <label htmlFor="new-email">NUS Email</label>
        <div className="input-affix">
          <input
            id="new-email"
            className="input"
            type="email"
            autoComplete="email"
            placeholder="you@u.nus.edu"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            aria-invalid={!emailOk ? true : undefined}
            required
          />
        </div>
        {!emailOk && (
          <p className="hint error">Use your NUS email ending in @u.nus.edu.</p>
        )}
      </div>

      <div className="field">
        <label htmlFor="new-password">Password</label>
        <input
          id="new-password"
          className="input"
          type="password"
          autoComplete="new-password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          required
        />
      </div>

      <p className="muted" style={{ fontSize: "0.85rem", margin: "0 0 0.3rem" }}>
        Must include:
      </p>
      <div className="pw-checks">
        {(Object.keys(CHECK_LABELS) as (keyof typeof checks)[]).map((key) => (
          <span key={key} className={`pw-check ${checks[key] ? "met" : ""}`}>
            <span className="pw-dot">{checks[key] ? "✓" : ""}</span>
            {CHECK_LABELS[key]}
          </span>
        ))}
      </div>

      <div className="field">
        <label htmlFor="confirm">Confirm password</label>
        <input
          id="confirm"
          className="input"
          type="password"
          autoComplete="new-password"
          value={confirm}
          onChange={(e) => setConfirm(e.target.value)}
          aria-invalid={!confirmOk ? true : undefined}
          required
        />
        {!confirmOk && <p className="hint error">Passwords do not match.</p>}
      </div>

      <div className="row-between">
        <label className="checkbox">
          <input
            type="checkbox"
            checked={agree}
            onChange={(e) => setAgree(e.target.checked)}
          />
          I agree to the{" "}
          <a href="#terms" onClick={(e) => e.preventDefault()}>
            terms of use
          </a>
        </label>
      </div>

      <button type="submit" className="btn" disabled={!canSubmit}>
        {busy ? "Creating…" : "Create account"}
      </button>
      {!dev && error && (
        <button type="button" className="link-btn mt" disabled={busy} onClick={() => {
          try { hostedLogin(true); } catch (err) { setError(err instanceof Error ? err.message : "Auth0 is unavailable."); }
        }}>
          Continue on the Auth0 page
        </button>
      )}
    </form>
  );
}
