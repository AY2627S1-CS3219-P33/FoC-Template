import { useState } from "react";
import { useAuth } from "../auth/AuthContext";
import { LoginForm } from "./auth/LoginForm";
import { CreateAccountForm } from "./auth/CreateAccountForm";
import { ResetPassword } from "./auth/ResetPassword";

type Mode = "login" | "create" | "reset";

export function AuthScreen() {
  const { error } = useAuth();
  const [mode, setMode] = useState<Mode>("login");
  const [loginEmail, setLoginEmail] = useState("");

  return (
    <main className="auth-shell">
      <div className="auth-stack">
        <div className="brand-logo">logo</div>
        {error && <div className="banner banner-error" role="alert">{error}</div>}

        {mode === "reset" ? (
          <ResetPassword onBack={() => setMode("login")} />
        ) : (
          <div className="auth-card">
            <div className="tabs" role="tablist" aria-label="Authentication">
              <button
                role="tab"
                aria-selected={mode === "login"}
                className="tab"
                onClick={() => setMode("login")}
              >
                Log in
              </button>
              <button
                role="tab"
                aria-selected={mode === "create"}
                className="tab"
                onClick={() => setMode("create")}
              >
                Create account
              </button>
            </div>
            <div className="tab-body" role="tabpanel">
              {mode === "login" ? (
                <LoginForm onForgot={() => setMode("reset")} initialEmail={loginEmail} />
              ) : (
                <CreateAccountForm onLogin={(email) => { setLoginEmail(email); setMode("login"); }} />
              )}
            </div>
          </div>
        )}
      </div>
    </main>
  );
}
