import { useState, type FormEvent } from "react";
import { useAuth } from "../../auth/AuthContext";

// Matches wireframe image7 (login) and image8 (error state).
export function LoginForm({ onForgot, dev }: { onForgot: () => void; dev: boolean }) {
  const { signInDev, signInAuth0 } = useAuth();
  const [identifier, setIdentifier] = useState("");
  const [password, setPassword] = useState("");
  const [showPassword, setShowPassword] = useState(false);
  const [remember, setRemember] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    setError(null);
    setBusy(true);
    try {
      if (dev) {
        await signInDev(identifier, remember);
      } else {
        await signInAuth0();
      }
    } catch {
      // The service does not reveal whether the account exists (NFR3.1.6).
      setError("Incorrect username or password. Please try again.");
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
        <label htmlFor="identifier">Username or email</label>
        <input
          id="identifier"
          className="input"
          type="text"
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
        <label className="checkbox">
          <input
            type="checkbox"
            checked={remember}
            onChange={(e) => setRemember(e.target.checked)}
          />
          Remember me
        </label>
        <button type="button" className="link-btn" onClick={onForgot}>
          Forgot password?
        </button>
      </div>

      <button type="submit" className="btn" disabled={busy}>
        {busy ? "Logging in…" : "Log in"}
      </button>
    </form>
  );
}
