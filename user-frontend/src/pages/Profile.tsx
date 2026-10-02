import { useMemo, useState, type FormEvent } from "react";
import { useAuth } from "../auth/AuthContext";
import { RequestError, updateProfile } from "../api/client";
import type { Profile as ProfileData } from "../api/types";
import { RoleBadge, CAPABILITIES } from "../components/RoleBadge";
import {
  DISPLAY_NAME_MAX,
  MOBILE_MAX,
  validateDisplayName,
  validateMobile,
} from "../validation";

function creditLabel(balance: number | null): string {
  return balance === null ? "Not available" : `${balance} credits`;
}

export function Profile() {
  const { profile, setProfile, getAccessToken } = useAuth();
  const account = profile as ProfileData;

  const [displayName, setDisplayName] = useState(account.display_name);
  const [mobile, setMobile] = useState(account.mobile_number);
  const [saving, setSaving] = useState(false);
  const [message, setMessage] = useState<{ kind: "success" | "error"; text: string } | null>(
    null,
  );

  const displayNameError = useMemo(() => validateDisplayName(displayName), [displayName]);
  const mobileError = useMemo(() => validateMobile(mobile), [mobile]);

  const dirty = displayName !== account.display_name || mobile !== account.mobile_number;
  const canSave = dirty && !displayNameError && !mobileError && !saving;

  async function onSave(event: FormEvent) {
    event.preventDefault();
    if (!canSave) return;
    setSaving(true);
    setMessage(null);
    try {
      const updated = await updateProfile(await getAccessToken(), {
        display_name: displayName !== account.display_name ? displayName : undefined,
        mobile_number: mobile !== account.mobile_number ? mobile : undefined,
      });
      setProfile(updated);
      setDisplayName(updated.display_name);
      setMobile(updated.mobile_number);
      setMessage({ kind: "success", text: "Profile updated." });
    } catch (err) {
      const text = err instanceof RequestError ? err.message : "Could not update profile.";
      setMessage({ kind: "error", text });
    } finally {
      setSaving(false);
    }
  }

  function onReset() {
    setDisplayName(account.display_name);
    setMobile(account.mobile_number);
    setMessage(null);
  }

  return (
    <>
      <div className="page-head">
        <div>
          <h1>{account.display_name || account.username}</h1>
          <div className="head-meta">
            <RoleBadge role={account.role} />
            <span className={`status-dot ${account.active ? "active" : "inactive"}`}>
              {account.active ? "Active" : "Inactive"}
            </span>
          </div>
        </div>
      </div>

      <p className="muted" style={{ marginTop: "-0.6rem" }}>
        {CAPABILITIES[account.role]}.
      </p>

      {message && (
        <div className={`banner banner-${message.kind}`} role="status">
          <span>{message.text}</span>
        </div>
      )}

      {/* Read-only account info (F1.4.1). Protected/system-managed (F1.4.3). */}
      <section className="panel">
        <h2>Account</h2>
        <dl className="fields">
          <div>
            <dt>Username</dt>
            <dd>{account.username}</dd>
          </div>
          <div>
            <dt>Email</dt>
            <dd>{account.email}</dd>
          </div>
          <div>
            <dt>
              Role <span className="locked" title="Managed by administrators">🔒</span>
            </dt>
            <dd>{account.role}</dd>
          </div>
          <div>
            <dt>
              Credit balance{" "}
              <span className="locked" title="Managed by the credit service">🔒</span>
            </dt>
            <dd>{creditLabel(account.credit_balance)}</dd>
          </div>
          <div className="full">
            <dt>Account ID</dt>
            <dd className="mono">{account.id}</dd>
          </div>
        </dl>
      </section>

      {/* Editable fields (F1.4.2). */}
      <form className="panel" onSubmit={onSave}>
        <h2>Edit profile</h2>

        <label htmlFor="display_name">Display name</label>
        <input
          id="display_name"
          className="input"
          type="text"
          value={displayName}
          maxLength={DISPLAY_NAME_MAX + 20}
          onChange={(e) => setDisplayName(e.target.value)}
          aria-invalid={displayNameError ? true : undefined}
        />
        <div className="field-foot">
          <span className="hint error">{displayNameError ?? ""}</span>
          <span className="counter">
            {displayName.length}/{DISPLAY_NAME_MAX}
          </span>
        </div>

        <label htmlFor="mobile_number">Mobile number</label>
        <input
          id="mobile_number"
          className="input"
          type="tel"
          value={mobile}
          maxLength={MOBILE_MAX + 20}
          onChange={(e) => setMobile(e.target.value)}
          aria-invalid={mobileError ? true : undefined}
        />
        <div className="field-foot">
          <span className="hint error">{mobileError ?? ""}</span>
          <span className="counter">
            {mobile.length}/{MOBILE_MAX}
          </span>
        </div>

        <div className="actions">
          <button type="submit" className="btn slim" disabled={!canSave}>
            {saving ? "Saving…" : "Save changes"}
          </button>
          <button
            type="button"
            className="btn outline slim"
            onClick={onReset}
            disabled={!dirty || saving}
          >
            Discard
          </button>
        </div>
      </form>
    </>
  );
}
