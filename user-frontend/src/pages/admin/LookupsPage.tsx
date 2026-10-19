import { useCallback, useEffect, useState, type FormEvent } from "react";
import {
  SupplierError,
  createBuilding,
  createSupplierType,
  listBuildings,
  listSupplierTypes,
  retireBuilding,
  retireSupplierType,
  type Lookup,
} from "../../api/supplier";

interface Props {
  getToken: (manage?: boolean) => Promise<string>;
}

type Kind = "buildings" | "types";

// The two lookup lists behave identically, so one component drives both and
// only the wording and the endpoints differ.
const CONFIG: Record<
  Kind,
  {
    title: string;
    singular: string;
    maxLength: number;
    placeholder: string;
    list: (token: string, includeRetired: boolean) => Promise<{ items: Lookup[] }>;
    create: (token: string, name: string) => Promise<Lookup>;
    retire: (token: string, id: string) => Promise<void>;
    conflictCode: SupplierError["code"];
    inUseCode: SupplierError["code"];
  }
> = {
  buildings: {
    title: "Buildings",
    singular: "building",
    maxLength: 120,
    placeholder: "e.g. COM 2",
    list: listBuildings,
    create: createBuilding,
    retire: retireBuilding,
    conflictCode: "BUILDING_CONFLICT",
    inUseCode: "BUILDING_IN_USE",
  },
  types: {
    title: "Supplier types",
    singular: "supplier type",
    maxLength: 64,
    placeholder: "e.g. Food",
    list: listSupplierTypes,
    create: createSupplierType,
    retire: retireSupplierType,
    conflictCode: "SUPPLIER_TYPE_CONFLICT",
    inUseCode: "SUPPLIER_TYPE_IN_USE",
  },
};

export function LookupsPage({ getToken }: Props) {
  const [kind, setKind] = useState<Kind>("buildings");

  return (
    <>
      <div className="page-head">
        <div>
          <h1>Buildings and types</h1>
          <p className="muted" style={{ margin: "0.3rem 0 0" }}>
            The managed lists suppliers and errands choose from. Names are permanent — there is no
            rename — and a retired row stays readable on records that already use it.
          </p>
        </div>
      </div>

      <div className="tabs" role="tablist">
        {(["buildings", "types"] as Kind[]).map((key) => (
          <button
            key={key}
            type="button"
            role="tab"
            className="tab"
            aria-selected={kind === key}
            onClick={() => setKind(key)}
          >
            {CONFIG[key].title}
          </button>
        ))}
      </div>

      <div className="tab-body">
        {/* Remounting on kind change resets the form and reloads the list. */}
        <LookupManager key={kind} kind={kind} getToken={getToken} />
      </div>
    </>
  );
}

function LookupManager({ kind, getToken }: { kind: Kind; getToken: Props["getToken"] }) {
  const config = CONFIG[kind];
  const [items, setItems] = useState<Lookup[]>([]);
  const [includeRetired, setIncludeRetired] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const [name, setName] = useState("");
  const [nameError, setNameError] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);

  const [retiring, setRetiring] = useState<Lookup | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const token = await getToken();
      const list = await config.list(token, includeRetired);
      setItems(list.items);
    } catch (err) {
      setError(messageFor(err, config.singular));
      setItems([]);
    } finally {
      setLoading(false);
    }
  }, [config, getToken, includeRetired]);

  useEffect(() => {
    void load();
  }, [load]);

  async function onCreate(event: FormEvent) {
    event.preventDefault();
    setNotice(null);
    setError(null);
    const trimmed = name.trim();
    // The service trims and requires a letter or digit; mirror that here.
    if (trimmed === "") {
      setNameError("A name is required.");
      return;
    }
    if (!/[\p{L}\p{N}]/u.test(trimmed)) {
      setNameError("Must contain a letter or a digit.");
      return;
    }
    if (trimmed.length > config.maxLength) {
      setNameError(`Must be at most ${config.maxLength} characters.`);
      return;
    }
    setCreating(true);
    setNameError(null);
    try {
      const token = await getToken(true);
      const created = await config.create(token, trimmed);
      setName("");
      setNotice(`Added ${created.name}.`);
      await load();
    } catch (err) {
      if (err instanceof SupplierError && err.code === config.conflictCode) {
        // The message names the existing row, which is the useful part: a
        // lookup key ignores case, spaces and punctuation, so "Com2" collides
        // with "COM 2".
        setNameError(err.message);
      } else {
        setNameError(messageFor(err, config.singular));
      }
    } finally {
      setCreating(false);
    }
  }

  async function onRetire() {
    if (!retiring) return;
    setBusy(true);
    setError(null);
    setNotice(null);
    try {
      const token = await getToken(true);
      await config.retire(token, retiring.id);
      setNotice(`${retiring.name} is retired and can no longer be assigned.`);
      setRetiring(null);
      await load();
    } catch (err) {
      if (err instanceof SupplierError && err.code === config.inUseCode) {
        setError(
          `${retiring.name} is still used by a supplier. Move those suppliers to another ${config.singular} first.`,
        );
      } else {
        setError(messageFor(err, config.singular));
      }
      setRetiring(null);
    } finally {
      setBusy(false);
    }
  }

  const live = items.filter((item) => !item.retired).length;

  return (
    <>
      {notice && (
        <div className="banner banner-success" role="status">
          <span>{notice}</span>
        </div>
      )}
      {error && (
        <div className="banner banner-error" role="alert">
          <span>{error}</span>
        </div>
      )}

      <form className="panel" onSubmit={onCreate}>
        <h2>Add a {config.singular}</h2>
        <div className="field">
          <label htmlFor="lookup-name">Name</label>
          <input
            id="lookup-name"
            className="input"
            value={name}
            maxLength={config.maxLength}
            placeholder={config.placeholder}
            onChange={(e) => {
              setName(e.target.value);
              setNameError(null);
            }}
            aria-invalid={!!nameError}
          />
          {nameError && <p className="hint error">{nameError}</p>}
          <p className="hint muted">
            This name is permanent and is what suppliers and errands display. Spaces, case and
            punctuation are ignored when checking for duplicates.
          </p>
        </div>
        <div className="actions">
          <button type="submit" className="btn slim" disabled={creating}>
            {creating ? "Adding…" : `Add ${config.singular}`}
          </button>
        </div>
      </form>

      <div className="toolbar">
        <label className="checkbox">
          <input
            type="checkbox"
            checked={includeRetired}
            onChange={(e) => setIncludeRetired(e.target.checked)}
          />
          Show retired
        </label>
        <button type="button" className="btn outline slim" onClick={load} disabled={loading}>
          Refresh
        </button>
      </div>

      {!loading && items.length === 0 && (
        <div className="placeholder">
          <p>No {config.title.toLowerCase()} yet.</p>
          <p className="muted">
            Add one above — suppliers cannot be created until at least one exists.
          </p>
        </div>
      )}

      {items.length > 0 && (
        <div className="admin-table-wrap">
          <table className="admin-table">
            <thead>
              <tr>
                <th scope="col">Name</th>
                <th scope="col">Status</th>
                <th scope="col" className="admin-action-cell">
                  Action
                </th>
              </tr>
            </thead>
            <tbody>
              {items.map((item) => (
                <tr key={item.id}>
                  <td>
                    {item.name}
                    <p className="admin-id">{item.id}</p>
                  </td>
                  <td>
                    <span className={`status-chip tone-${item.retired ? "bad" : "good"}`}>
                      {item.retired ? "Retired" : "Live"}
                    </span>
                  </td>
                  <td className="admin-action-cell">
                    {item.retired ? (
                      <span className="muted small">—</span>
                    ) : (
                      <button
                        type="button"
                        className="btn outline slim"
                        onClick={() => setRetiring(item)}
                      >
                        Retire
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {loading && <p className="muted center mt">Loading…</p>}

      {items.length > 0 && (
        <p className="muted small mt">
          {live} live{includeRetired ? ` · ${items.length - live} retired` : ""}. Capped at 500 rows.
        </p>
      )}

      {retiring && (
        <div className="modal-backdrop" role="dialog" aria-modal="true" aria-label="Retire">
          <div className="modal">
            <h2>Retire {retiring.name}?</h2>
            <p className="muted">
              It disappears from the pickers, so no new supplier or errand can use it. Suppliers and
              errands that already reference it keep showing it. The name is released, so a new row
              could reuse it later. This is rejected while a live supplier still uses it.
            </p>
            <div className="actions">
              <button type="button" className="btn danger slim" onClick={onRetire} disabled={busy}>
                {busy ? "Retiring…" : "Retire"}
              </button>
              <button
                type="button"
                className="btn outline slim"
                onClick={() => setRetiring(null)}
                disabled={busy}
              >
                Cancel
              </button>
            </div>
          </div>
        </div>
      )}
    </>
  );
}

function messageFor(err: unknown, singular: string): string {
  if (err instanceof SupplierError) {
    if (err.code === "UNAUTHENTICATED") return "Please sign in again.";
    if (err.code === "FORBIDDEN") return `You need administrator rights to manage ${singular}s.`;
    if (err.code === "DEPENDENCY_UNAVAILABLE")
      return "The supplier service is temporarily unavailable.";
    return err.message;
  }
  return "Could not reach the supplier service.";
}
