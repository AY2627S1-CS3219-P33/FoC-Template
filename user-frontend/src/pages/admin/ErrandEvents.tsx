import { useCallback, useState, type FormEvent } from "react";
import { listErrandEvents, type ErrandStatus, type TransitionRecord } from "../../api/order";
import { usePagedList } from "../errands/usePagedList";
import { errandError } from "../errands/messages";
import { formatDateTime, statusClass, statusLabel } from "../errands/format";
import { fromLocalInput } from "../../orderValidation";

interface Props {
  getToken: () => Promise<string>;
}

const PAGE_SIZE = 25;

const STATUSES: ErrandStatus[] = [
  "PENDING_RESERVATION",
  "OPEN",
  "ASSIGNED",
  "PICKED_UP",
  "DELIVERED",
  "REVIEW_REQUIRED",
  "REJECTED",
  "CANCELLED",
  "EXPIRED",
  "COMPLETED",
  "REFUNDED",
];

interface Filters {
  userId: string;
  errandId: string;
  type: "" | ErrandStatus;
  from: string;
  to: string;
}

const EMPTY: Filters = { userId: "", errandId: "", type: "", from: "", to: "" };

export function ErrandEvents({ getToken }: Props) {
  // Typing in the inputs must not refetch on every keystroke, and a cursor is
  // bound to the filters that produced it, so the applied set is separate.
  const [inputs, setInputs] = useState<Filters>(EMPTY);
  const [applied, setApplied] = useState<Filters>(EMPTY);

  const load = useCallback(
    async (cursor?: string) => {
      const token = await getToken();
      return listErrandEvents(token, {
        userId: applied.userId || undefined,
        errandId: applied.errandId || undefined,
        type: applied.type || undefined,
        from: applied.from ? fromLocalInput(applied.from) : undefined,
        to: applied.to ? fromLocalInput(applied.to) : undefined,
        limit: PAGE_SIZE,
        cursor,
      });
    },
    [getToken, applied],
  );

  const list = usePagedList<TransitionRecord>(load, errandError);

  function apply(event: FormEvent) {
    event.preventDefault();
    setApplied(inputs);
  }

  function set<K extends keyof Filters>(key: K, value: Filters[K]) {
    setInputs((f) => ({ ...f, [key]: value }));
  }

  return (
    <>
      <form className="panel" onSubmit={apply}>
        <h2>Filters</h2>
        <p className="muted small">Filters combine with AND. Leave a field blank to ignore it.</p>
        <div className="form-grid mt">
          <div className="field">
            <label htmlFor="event-user">User ID</label>
            <input
              id="event-user"
              className="input"
              value={inputs.userId}
              placeholder="auth0|…"
              onChange={(e) => set("userId", e.target.value)}
            />
            <p className="hint muted">Requester, courier, or whoever caused the transition.</p>
          </div>
          <div className="field">
            <label htmlFor="event-errand">Errand ID</label>
            <input
              id="event-errand"
              className="input"
              value={inputs.errandId}
              placeholder="UUID"
              onChange={(e) => set("errandId", e.target.value)}
            />
          </div>
          <div className="field">
            <label htmlFor="event-type">Moved into</label>
            <select
              id="event-type"
              className="input select"
              value={inputs.type}
              onChange={(e) => set("type", e.target.value as "" | ErrandStatus)}
            >
              <option value="">Any state</option>
              {STATUSES.map((status) => (
                <option key={status} value={status}>
                  {statusLabel(status)}
                </option>
              ))}
            </select>
          </div>
          <div className="field">
            <label htmlFor="event-from">From</label>
            <input
              id="event-from"
              className="input"
              type="datetime-local"
              value={inputs.from}
              onChange={(e) => set("from", e.target.value)}
            />
          </div>
          <div className="field">
            <label htmlFor="event-to">To</label>
            <input
              id="event-to"
              className="input"
              type="datetime-local"
              value={inputs.to}
              onChange={(e) => set("to", e.target.value)}
            />
            <p className="hint muted">Exclusive upper bound.</p>
          </div>
        </div>
        <div className="actions">
          <button type="submit" className="btn slim">
            Apply filters
          </button>
          <button
            type="button"
            className="btn outline slim"
            onClick={() => {
              setInputs(EMPTY);
              setApplied(EMPTY);
            }}
          >
            Clear
          </button>
        </div>
      </form>

      {list.error && (
        <div className="banner banner-error" role="alert">
          <span>{list.error}</span>
        </div>
      )}

      {!list.error && !list.loading && list.items.length === 0 && (
        <div className="placeholder">
          <p>No transitions match these filters.</p>
        </div>
      )}

      {list.items.length > 0 && (
        <div className="admin-table-wrap">
          <table className="admin-table">
            <thead>
              <tr>
                <th scope="col">When</th>
                <th scope="col">Transition</th>
                <th scope="col">Event</th>
                <th scope="col">Actor</th>
                <th scope="col">Errand</th>
                <th scope="col">Ver.</th>
              </tr>
            </thead>
            <tbody>
              {list.items.map((row) => (
                <tr key={row.id}>
                  <td>{formatDateTime(row.at)}</td>
                  <td>
                    {row.fromStatus && (
                      <>
                        <span className="muted small">{statusLabel(row.fromStatus)}</span>
                        <span className="muted" aria-hidden>
                          {" → "}
                        </span>
                      </>
                    )}
                    <span className={statusClass(row.toStatus)}>{statusLabel(row.toStatus)}</span>
                  </td>
                  <td>
                    <code className="mono small">{row.event}</code>
                    {row.reason && <p className="muted small">{row.reason}</p>}
                  </td>
                  <td>
                    <span className="type-chip">{row.actorType}</span>
                    {row.actorId && <p className="admin-id">{row.actorId}</p>}
                  </td>
                  <td className="admin-id">{row.errandId}</td>
                  <td className="mono">{row.errandVersion}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {list.loading && <p className="muted center mt">Loading…</p>}

      {list.hasMore && !list.loading && (
        <div className="center mt">
          <button type="button" className="btn outline slim" onClick={list.loadMore}>
            Load more
          </button>
        </div>
      )}
    </>
  );
}
