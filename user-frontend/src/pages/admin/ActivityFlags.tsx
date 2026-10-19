import { useCallback, useEffect, useState } from "react";
import { listFlags, type Flag } from "../../api/order";
import { errandError } from "../errands/messages";
import { formatDateTime, formatDuration } from "../errands/format";

interface Props {
  getToken: () => Promise<string>;
}

const KINDS: Record<Flag["kind"], string> = {
  COURIER_WITHDRAWALS: "Withdrawals as courier",
  REQUESTER_CANCELLATIONS: "Cancellations as requester",
};

// Flags are evaluated when the list is built, against the current settings, and
// the endpoint is unpaginated (capped at 500 rows).
export function ActivityFlags({ getToken }: Props) {
  const [flags, setFlags] = useState<Flag[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const token = await getToken();
      const list = await listFlags(token);
      setFlags(list.items);
    } catch (err) {
      setError(errandError(err));
      setFlags([]);
    } finally {
      setLoading(false);
    }
  }, [getToken]);

  useEffect(() => {
    void load();
  }, [load]);

  return (
    <>
      <div className="toolbar">
        <button type="button" className="btn outline slim" onClick={load} disabled={loading}>
          Refresh
        </button>
      </div>

      {error && (
        <div className="banner banner-error" role="alert">
          <span>{error}</span>
        </div>
      )}

      {!error && !loading && flags.length === 0 && (
        <div className="placeholder">
          <p>No suspicious activity right now.</p>
          <p className="muted">
            Couriers who hit the withdrawal restriction and requesters who exceed the cancellation
            threshold appear here, most active first.
          </p>
        </div>
      )}

      {flags.length > 0 && (
        <div className="admin-table-wrap">
          <table className="admin-table">
            <thead>
              <tr>
                <th scope="col">User</th>
                <th scope="col">Kind</th>
                <th scope="col">Trigger</th>
                <th scope="col">Count</th>
                <th scope="col">Window</th>
                <th scope="col">Since</th>
              </tr>
            </thead>
            <tbody>
              {flags.map((flag) => (
                <tr key={`${flag.userId}-${flag.kind}`}>
                  <td className="admin-id">{flag.userId}</td>
                  <td>{KINDS[flag.kind] ?? flag.kind}</td>
                  <td>{flag.summary}</td>
                  <td className="mono">
                    {flag.count} / {flag.threshold}
                  </td>
                  <td>{formatDuration(flag.window)}</td>
                  <td>{formatDateTime(flag.since)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {loading && <p className="muted center mt">Loading…</p>}
    </>
  );
}
