import { useCallback, useState } from "react";
import {
  listReviews,
  resolveReview,
  type AdminErrandView,
  type ReviewReason,
} from "../../api/order";
import { validateReason, LIMITS } from "../../orderValidation";
import { usePagedList } from "../errands/usePagedList";
import { errandError, isStale } from "../errands/messages";
import {
  creditText,
  formatDateTime,
  itemSummary,
  reviewReasonLabel,
  statusClass,
  statusLabel,
} from "../errands/format";

interface Props {
  getToken: () => Promise<string>;
}

const PAGE_SIZE = 20;

const REASONS: { value: "" | ReviewReason; label: string }[] = [
  { value: "", label: "All reasons" },
  { value: "COURIER_ISSUE", label: "Courier reported a problem" },
  { value: "REQUESTER_DISPUTE", label: "Requester disputed" },
  { value: "DELIVERY_OVERDUE", label: "Delivery overdue" },
];

// Resolving needs an outcome and a reason, and sends the version the admin was
// shown; a second resolution of the same errand is rejected and has no effect.
type Resolving = { errand: AdminErrandView; outcome: "COMPLETED" | "REFUNDED" } | null;

export function ReviewQueue({ getToken }: Props) {
  const [reason, setReason] = useState<"" | ReviewReason>("");
  const [resolving, setResolving] = useState<Resolving>(null);
  const [note, setNote] = useState("");
  const [noteError, setNoteError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [actionError, setActionError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const load = useCallback(
    async (cursor?: string) => {
      const token = await getToken();
      return listReviews(token, {
        reason: reason || undefined,
        limit: PAGE_SIZE,
        cursor,
      });
    },
    [getToken, reason],
  );

  const list = usePagedList<AdminErrandView>(load, errandError);

  async function submit() {
    if (!resolving) return;
    const problem = validateReason(note);
    if (problem) {
      setNoteError(problem);
      return;
    }
    setBusy(true);
    setActionError(null);
    try {
      const token = await getToken();
      await resolveReview(
        token,
        resolving.errand.errandId,
        resolving.outcome,
        note.trim(),
        resolving.errand.version,
      );
      setNotice(
        resolving.outcome === "COMPLETED"
          ? "Resolved: the reward went to the courier."
          : "Resolved: the reservation was refunded to the requester.",
      );
      setResolving(null);
      setNote("");
      list.reload();
    } catch (err) {
      setActionError(errandError(err));
      if (isStale(err)) {
        setResolving(null);
        list.reload();
      }
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <div className="toolbar">
        <select
          className="input select"
          value={reason}
          onChange={(e) => setReason(e.target.value as "" | ReviewReason)}
          aria-label="Filter by review reason"
        >
          {REASONS.map((option) => (
            <option key={option.value} value={option.value}>
              {option.label}
            </option>
          ))}
        </select>
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
          <p>Nothing is waiting for review.</p>
          <p className="muted">
            Disputes, courier problems and overdue deliveries appear here, oldest first.
          </p>
        </div>
      )}

      {list.items.map((errand) => (
        <section className="panel" key={errand.errandId}>
          <div className="row-between">
            <div>
              <h2 style={{ marginBottom: "0.3rem" }}>{errand.pickup.name}</h2>
              <div className="head-meta">
                <span className={statusClass(errand.status)}>{statusLabel(errand.status)}</span>
                <span className="reward-chip">{creditText(errand.reward)}</span>
                <span className="type-chip">{reviewReasonLabel(errand.review.reason)}</span>
              </div>
            </div>
          </div>

          <dl className="fields mt">
            <div className="full">
              <dt>Items</dt>
              <dd>{itemSummary(errand.items)}</dd>
            </div>
            <div className="full">
              <dt>Drop-off</dt>
              <dd>
                {errand.deliveryBuilding.name} — {errand.deliveryDetail}
              </dd>
            </div>
            <div className="full">
              <dt>Reported issue</dt>
              <dd>{errand.review.note || <span className="muted">No note given.</span>}</dd>
            </div>
            <div>
              <dt>Raised</dt>
              <dd>{formatDateTime(errand.review.createdAt)}</dd>
            </div>
            <div>
              <dt>Reported by</dt>
              <dd className="mono">
                {errand.review.reporterId ?? <span className="muted">System</span>}
              </dd>
            </div>
            <div>
              <dt>Requester</dt>
              <dd className="mono">{errand.requesterId}</dd>
            </div>
            <div>
              <dt>Courier</dt>
              <dd className="mono">{errand.courierId ?? <span className="muted">None</span>}</dd>
            </div>
            <div className="full">
              <dt>Errand ID</dt>
              <dd className="mono">{errand.errandId}</dd>
            </div>
          </dl>

          {errand.review.resolution ? (
            <p className="muted small">
              Already resolved as {errand.review.resolution.toLowerCase()} on{" "}
              {errand.review.resolvedAt ? formatDateTime(errand.review.resolvedAt) : "an earlier date"}.
            </p>
          ) : (
            <div className="actions">
              <button
                type="button"
                className="btn slim"
                onClick={() => {
                  setResolving({ errand, outcome: "COMPLETED" });
                  setNote("");
                  setNoteError(null);
                  setActionError(null);
                }}
              >
                Complete — pay the courier
              </button>
              <button
                type="button"
                className="btn danger slim"
                onClick={() => {
                  setResolving({ errand, outcome: "REFUNDED" });
                  setNote("");
                  setNoteError(null);
                  setActionError(null);
                }}
              >
                Refund the requester
              </button>
            </div>
          )}
        </section>
      ))}

      {list.loading && <p className="muted center mt">Loading…</p>}

      {list.hasMore && !list.loading && (
        <div className="center mt">
          <button type="button" className="btn outline slim" onClick={list.loadMore}>
            Load more
          </button>
        </div>
      )}

      {resolving && (
        <div className="modal-backdrop" role="dialog" aria-modal="true" aria-label="Resolve review">
          <div className="modal">
            <h2>
              {resolving.outcome === "COMPLETED" ? "Pay the courier?" : "Refund the requester?"}
            </h2>
            <p className="muted">
              {resolving.outcome === "COMPLETED"
                ? `This transfers ${creditText(resolving.errand.reward)} to the courier and closes the errand as completed.`
                : `This releases ${creditText(resolving.errand.reward)} back to the requester and closes the errand as refunded.`}{" "}
              The reason is recorded against the review and in the audit log.
            </p>
            <div className="field">
              <label htmlFor="resolution-reason">Resolution reason</label>
              <textarea
                id="resolution-reason"
                className="input"
                rows={4}
                value={note}
                maxLength={LIMITS.reason}
                onChange={(e) => {
                  setNote(e.target.value);
                  setNoteError(null);
                }}
                aria-invalid={!!noteError}
              />
              {noteError && <p className="hint error">{noteError}</p>}
            </div>
            <div className="actions">
              <button
                type="button"
                className={`btn slim${resolving.outcome === "REFUNDED" ? " danger" : ""}`}
                onClick={submit}
                disabled={busy}
              >
                {busy ? "Resolving…" : "Confirm"}
              </button>
              <button
                type="button"
                className="btn outline slim"
                onClick={() => setResolving(null)}
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
