import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { listAdminUsers, RequestError, setAdministrator } from "../../api/client";
import type { AdminUser } from "../../api/types";
import { useAuth } from "../../auth/AuthContext";

type RoleTab = "administrator" | "user";
type Access = "checking" | "allowed" | "denied";

export function AdminUsersPage() {
  const { getAccessToken, signOut } = useAuth();
  const [role, setRole] = useState<RoleTab>("administrator");
  const [emailInput, setEmailInput] = useState("");
  const [email, setEmail] = useState("");
  const [users, setUsers] = useState<AdminUser[]>([]);
  const [nextCursor, setNextCursor] = useState("");
  const [loading, setLoading] = useState(false);
  const [busyUser, setBusyUser] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [access, setAccess] = useState<Access>("checking");
  const requestID = useRef(0);

  const load = useCallback(async (cursor?: string) => {
    const id = ++requestID.current;
    setLoading(true);
    setError(null);
    try {
      const token = await getAccessToken({ scope: "openid profile email users:manage:roles" });
      const page = await listAdminUsers(token, role, email, cursor);
      if (id !== requestID.current) return;
      setAccess("allowed");
      setUsers((current) => cursor ? [...current, ...page.users] : page.users);
      setNextCursor(page.next_cursor ?? "");
    } catch (err) {
      if (id !== requestID.current) return;
      if (err instanceof RequestError && (err.status === 401 || err.status === 403)) {
        setAccess("denied");
      }
      setError(adminError(err));
      setUsers([]);
      setNextCursor("");
    } finally {
      if (id === requestID.current) setLoading(false);
    }
  }, [email, getAccessToken, role]);

  useEffect(() => {
    setUsers([]);
    setNextCursor("");
    void load();
    return () => { requestID.current += 1; };
  }, [load]);

  function search(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setNotice(null);
    setEmail(emailInput.trim());
  }

  async function changeRole(user: AdminUser, enabled: boolean) {
    const action = enabled ? "promote" : "demote";
    if (!window.confirm(`Are you sure you want to ${action} ${user.email}${enabled ? " to Administrator" : " from Administrator"}?`)) return;
    setBusyUser(user.user_id);
    setError(null);
    setNotice(null);
    try {
      const token = await getAccessToken({ scope: "openid profile email users:manage:roles" });
      await setAdministrator(token, user.user_id, enabled);
      setNotice(`${user.email} ${enabled ? "is now an Administrator" : "was demoted to User"}.`);
      await load();
    } catch (err) {
      setError(adminError(err));
    } finally {
      setBusyUser("");
    }
  }

  if (access !== "allowed") {
    return (
      <main className="auth-shell">
        <section className="panel admin-access-card" aria-live="polite">
          {access === "checking" && loading ? (
            <p className="muted">Checking administrator access…</p>
          ) : access === "denied" ? (
            <>
              <h1>Administrator access required</h1>
              <p className="muted">This account does not have permission to open the admin dashboard.</p>
              <a className="btn admin-return" href="/">Return to Friend on Campus</a>
            </>
          ) : (
            <>
              <h1>Could not verify access</h1>
              <p className="muted">{error ?? "The user service is unavailable."}</p>
              <button className="btn slim" type="button" onClick={() => void load()}>Try again</button>
              <a className="admin-return-link" href="/">Return to Friend on Campus</a>
            </>
          )}
        </section>
      </main>
    );
  }

  return (
    <main className="admin-page">
      <header className="admin-header">
        <div>
          <a href="/" className="admin-back">← Friend on Campus</a>
          <p className="admin-eyebrow">ACCESS MANAGEMENT</p>
          <h1>Users</h1>
          <p className="muted">Manage account roles stored in Auth0.</p>
        </div>
        <button type="button" className="btn outline slim" onClick={signOut}>Log out</button>
      </header>

      <section className="panel admin-panel" aria-label="User directory">
        <div className="admin-tabs" role="tablist" aria-label="User role">
          {(["administrator", "user"] as const).map((item) => (
            <button key={item} type="button" role="tab" aria-selected={role === item}
              className="admin-tab" onClick={() => setRole(item)}>
              {item === "administrator" ? "Administrators" : "Users"}
            </button>
          ))}
        </div>

        <form className="admin-search" onSubmit={search}>
          <label className="admin-search-label" htmlFor="admin-email">Search by email</label>
          <div className="admin-search-controls">
            <input id="admin-email" className="input" type="email" autoComplete="off"
              placeholder="name@example.com" value={emailInput}
              onChange={(event) => setEmailInput(event.target.value)} />
            <button className="btn slim" type="submit" disabled={loading}>Search</button>
            {email !== "" && <button className="btn outline slim" type="button"
              onClick={() => { setEmailInput(""); setEmail(""); }}>Clear</button>}
          </div>
          {email && <p className="hint muted">Exact email match: {email}</p>}
        </form>

        {error && <div className="banner banner-error" role="alert">{error}</div>}
        {notice && <div className="banner banner-success" role="status">{notice}</div>}

        <div className="admin-table-wrap">
          <table className="admin-table">
            <thead><tr><th scope="col">User</th><th scope="col">User ID</th><th scope="col"><span className="sr-only">Actions</span></th></tr></thead>
            <tbody>
              {users.map((user) => (
                <tr key={user.user_id}>
                  <td><strong>{user.name || user.email || "Unnamed user"}</strong><span className="admin-user-email">{user.email}</span></td>
                  <td><code className="admin-id">{user.user_id}</code></td>
                  <td className="admin-action-cell">
                    <button type="button" className={`btn slim ${role === "administrator" ? "outline" : ""}`}
                      disabled={Boolean(busyUser) || loading} onClick={() => void changeRole(user, role === "user")}>
                      {busyUser === user.user_id ? "Updating…" : role === "administrator" ? "Demote" : "Make admin"}
                    </button>
                  </td>
                </tr>
              ))}
              {!loading && users.length === 0 && !error && (
                <tr><td colSpan={3} className="admin-empty">{email ? "No matching users." : "No users in this list."}</td></tr>
              )}
              {loading && users.length === 0 && <tr><td colSpan={3} className="admin-empty">Loading users…</td></tr>}
            </tbody>
          </table>
        </div>

        {nextCursor && <div className="admin-more">
          <button type="button" className="btn outline slim" disabled={loading} onClick={() => void load(nextCursor)}>
            {loading ? "Loading…" : "Load more"}
          </button>
        </div>}
      </section>
      <p className="admin-footnote">Role changes take effect in Auth0. The user may need to sign in again to receive an updated access token.</p>
    </main>
  );
}

function adminError(error: unknown): string {
  if (error instanceof RequestError) {
    if (error.status === 401) return "Your session has expired. Sign in again to continue.";
    if (error.status === 403) return "You do not have permission to manage users. Sign in with an Auth0 Administrator account.";
    if (error.status === 503) return "The Auth0 user directory is temporarily unavailable.";
    return error.message;
  }
  return "Could not reach the user service. Check that it is running and try again.";
}
