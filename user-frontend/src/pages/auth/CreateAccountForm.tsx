import { useMemo, useState, type FormEvent } from "react";
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
export function CreateAccountForm({ dev }: { dev: boolean }) {
  const { signUpDev, signInAuth0 } = useAuth();
  const [username, setUsername] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [agree, setAgree] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

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
    if (!canSubmit) return;
    setError(null);
    setBusy(true);
    try {
      if (dev) {
        await signUpDev(username, email, false);
      } else {
        // Production account creation is handled by Auth0 Universal Login.
        await signInAuth0();
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not create account.");
      setBusy(false);
    }
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
            placeholder="username"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            aria-invalid={!emailOk ? true : undefined}
            required
          />
          <span className="suffix">@u.nus.edu</span>
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
    </form>
  );
}
