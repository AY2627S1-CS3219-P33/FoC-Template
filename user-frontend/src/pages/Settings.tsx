import { useAuth } from "../auth/AuthContext";
import type { Profile as ProfileData } from "../api/types";

// Minimal account settings within User Service scope. Session ends here (F1.3.1).
export function Settings() {
  const { profile, signOut } = useAuth();
  const account = profile as ProfileData;

  return (
    <>
      <div className="page-head">
        <h1>Settings</h1>
      </div>

      <section className="panel">
        <h2>Session</h2>
        <p className="muted" style={{ marginTop: 0 }}>
          Signed in as <b>{account.username}</b> ({account.email}).
        </p>
        <div className="actions">
          <button type="button" className="btn slim" onClick={signOut}>
            Log out
          </button>
        </div>
      </section>

      <section className="panel">
        <h2>Appearance</h2>
        <p className="muted" style={{ margin: 0 }}>
          The interface follows your system light/dark preference.
        </p>
      </section>
    </>
  );
}
