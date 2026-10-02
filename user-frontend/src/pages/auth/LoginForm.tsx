import { useRef, useState, type FormEvent } from "react";
import { useAuth } from "../../auth/AuthContext";

// Matches wireframe image7 (login) and image8 (error state).
export function LoginForm({ onForgot, dev, initialEmail = "" }: { onForgot: () => void; dev: boolean; initialEmail?: string }) {
  const { signInDev, signInAuth0, hostedLogin } = useAuth();
  const [identifier, setIdentifier] = useState(initialEmail);
  const [password, setPassword] = useState("");
  const [showPassword, setShowPassword] = useState(false);
  const [remember, setRemember] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const submitting = useRef(false);

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    if (submitting.current) return;
    submitting.current = true;
    setError(null);
    setBusy(true);
    try {
      if (dev) {
        await signInDev(identifier, remember);
      } else {
        await signInAuth0(identifier, password);
      }
    } catch (err) {
      setPassword("");
      setError(err instanceof Error ? err.message : "Could not sign in. Please try again.");
    } finally {
      submitting.current = false;
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
        <label htmlFor="identifier">{dev ? "Username or email" : "Email"}</label>
        <input
          id="identifier"
          className="input"
          type={dev ? "text" : "email"}
          autoComplete="username"
          value={identifier}
          onChange={(e) => setIdentifier(e.target.value)}
          aria-invalid={error ? true : undefined}
          required
        />
      </div>

      <div className="field">
        <label htmlFor="password">Password</label>
        <div className="input-affix">
          <input
            id="password"
            className="input"
            type={showPassword ? "text" : "password"}
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            aria-invalid={error ? true : undefined}
            required
          />
          <button
            type="button"
            className="link-btn"
            onClick={() => setShowPassword((v) => !v)}
          >
            {showPassword ? "hide" : "show"}
          </button>
        </div>
      </div>

      <div className="row-between">
        {dev && <label className="checkbox">
          <input
            type="checkbox"
            checked={remember}
            onChange={(e) => setRemember(e.target.checked)}
          />
          Remember me
        </label>}
        <button type="button" className="link-btn" onClick={onForgot}>
          Forgot password?
        </button>
      </div>

      <button type="submit" className="btn" disabled={busy}>
        {busy ? "Logging in…" : "Log in"}
      </button>
      {!dev && (
        <button type="button" className="link-btn mt" disabled={busy} onClick={() => {
          try { hostedLogin(); } catch (err) { setError(err instanceof Error ? err.message : "Auth0 is unavailable."); }
        }}>
          Continue on the Auth0 page
        </button>
      )}
    </form>
  );
}
