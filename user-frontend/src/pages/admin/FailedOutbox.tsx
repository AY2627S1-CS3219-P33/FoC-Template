import { useCallback, useState } from "react";
import { listFailedOutboxRows, requeueOutboxRow, type OutboxRow } from "../../api/order";
import { validateReason, LIMITS } from "../../orderValidation";
import { usePagedList } from "../errands/usePagedList";
import { errandError } from "../errands/messages";
import { formatDateTime } from "../errands/format";

interface Props {
  getToken: () => Promise<string>;
}

const PAGE_SIZE = 25;

export function FailedOutbox({ getToken }: Props) {
  const [requeuing, setRequeuing] = useState<OutboxRow | null>(null);
  const [reason, setReason] = useState("");
  const [reasonError, setReasonError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [actionError, setActionError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const load = useCallback(
    async (cursor?: string) => {
      const token = await getToken();
      return listFailedOutboxRows(token, { limit: PAGE_SIZE, cursor });
    },
    [getToken],
  );

  const list = usePagedList<OutboxRow>(load, errandError);

  async function submit() {
    if (!requeuing) return;
    const problem = validateReason(reason);
    if (problem) {
      setReasonError(problem);
      return;
    }
    setBusy(true);
    setActionError(null);
    try {
      const token = await getToken();
      await requeueOutboxRow(token, requeuing.id, reason.trim());
      setNotice(`Row ${requeuing.id} is pending again and will be re-sent.`);
      setRequeuing(null);
      setReason("");
      list.reload();
    } catch (err) {
      setActionError(errandError(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <div className="toolbar">
        <button type="button" className="btn outline slim" onClick={list.reload} disabled={list.loading}>
          Refresh
        </button>
      </div>

      {notice && (
        <div className="banner banner-success" role="status">
          <span>{notice}</span>
        </div>
      )}
      {(list.error || actionError) && (
        <div className="banner banner-error" role="alert">
          <span>{actionError ?? list.error}</span>
        </div>
      )}

      {!list.error && !list.loading && list.items.length === 0 && (
        <div className="placeholder">
          <p>No failed messages.</p>
          <p className="muted">
            Credit commands and events that exhausted their retries collect here for a human to
            replay.
          </p>
        </div>
      )}

      {list.items.length > 0 && (
        <>
          <p className="muted small">
            Requeuing keeps the row's idempotency key, so the replayed message has the same ID and
            cannot apply its effect twice. Payloads are never returned by the service.
          </p>
          <div className="admin-table-wrap">
            <table className="admin-table">
              <thead>
                <tr>
                  <th scope="col">ID</th>
                  <th scope="col">Kind</th>
                  <th scope="col">Errand</th>
                  <th scope="col">Attempts</th>
                  <th scope="col">Last failure</th>
                  <th scope="col">Created</th>
                  <th scope="col" className="admin-action-cell">
                    Action
                  </th>
                </tr>
              </thead>
              <tbody>
                {list.items.map((row) => (
                  <tr key={row.id}>
                    <td className="mono">{row.id}</td>
                    <td>
                      <code className="mono small">{row.kind}</code>
                    </td>
                    <td className="admin-id">{row.errandId}</td>
                    <td className="mono">{row.attempts}</td>
                    <td>
                      {row.lastFailureCode ? (
                        <code className="mono small">{row.lastFailureCode}</code>
                      ) : (
                        <span className="muted">—</span>
                      )}
                    </td>
                    <td>{formatDateTime(row.createdAt)}</td>
                    <td className="admin-action-cell">
                      <button
                        type="button"
                        className="btn outline slim"
                        onClick={() => {
                          setRequeuing(row);
                          setReason("");
                          setReasonError(null);
                          setActionError(null);
                        }}
                      >
                        Requeue
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      )}

      {list.loading && <p className="muted center mt">Loading…</p>}

      {list.hasMore && !list.loading && (
        <div className="center mt">
          <button type="button" className="btn outline slim" onClick={list.loadMore}>
            Load more
          </button>
        </div>
      )}

      {requeuing && (
        <div className="modal-backdrop" role="dialog" aria-modal="true" aria-label="Requeue row">
          <div className="modal">
            <h2>Requeue row {requeuing.id}?</h2>
            <p className="muted">
              This resets <code className="mono">{requeuing.kind}</code> to pending with its
              attempts cleared. The reason is recorded in the audit log.
            </p>
            <div className="field">
              <label htmlFor="requeue-reason">Reason</label>
              <textarea
                id="requeue-reason"
                className="input"
                rows={3}
                value={reason}
                maxLength={LIMITS.reason}
                onChange={(e) => {
                  setReason(e.target.value);
                  setReasonError(null);
                }}
                aria-invalid={!!reasonError}
              />
              {reasonError && <p className="hint error">{reasonError}</p>}
            </div>
            <div className="actions">
              <button type="button" className="btn slim" onClick={submit} disabled={busy}>
                {busy ? "Requeuing…" : "Requeue"}
              </button>
              <button
                type="button"
                className="btn outline slim"
                onClick={() => setRequeuing(null)}
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
