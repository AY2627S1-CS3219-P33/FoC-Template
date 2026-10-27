import { useCallback, useEffect, useState } from "react";
import {
  acceptErrand,
  cancelErrand,
  confirmErrand,
  deliverErrand,
  disputeErrand,
  getErrand,
  pickUpErrand,
  reportIssue,
  withdrawErrand,
  TERMINAL_STATUSES,
  type ErrandView,
  type RequesterErrandView,
} from "../../api/order";
import { validateReason, LIMITS } from "../../orderValidation";
import { errandError, isStale } from "./messages";
import {
  creditText,
  formatDateTime,
  itemCount,
  reviewReasonLabel,
  statusClass,
  statusLabel,
  timeLeft,
} from "./format";

interface Props {
  errandId: string;
  getToken: () => Promise<string>;
  onBack: () => void;
  onEdit: (errand: RequesterErrandView) => void;
  onOrderAgain: (errand: RequesterErrandView) => void;
  // Lets the surrounding lists refresh after a transition.
  onChanged: () => void;
}

// An action that needs a typed reason (dispute, report issue) opens a prompt;
// the rest run on one click.
type Prompt = { verb: "dispute" | "report-issue"; title: string; label: string } | null;

const REJECTION_TEXT: Record<string, string> = {
  INSUFFICIENT_CREDITS: "You did not have enough available credits for this reward.",
  ACCOUNT_NOT_FOUND: "No credit account was found for your profile.",
  RESERVATION_RELEASED: "The credit reservation was released before the errand opened.",
  RESERVATION_FAILED: "The credit service did not answer in time, so nothing was reserved.",
};

export function ErrandDetail({
  errandId,
  getToken,
  onBack,
  onEdit,
  onOrderAgain,
  onChanged,
}: Props) {
  const [errand, setErrand] = useState<ErrandView | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [prompt, setPrompt] = useState<Prompt>(null);
  const [reason, setReason] = useState("");
  const [reasonError, setReasonError] = useState<string | null>(null);

  const reload = useCallback(async () => {
    setLoading(true);
    try {
      const token = await getToken();
      setErrand(await getErrand(token, errandId));
      setError(null);
    } catch (err) {
      setError(errandError(err));
    } finally {
      setLoading(false);
    }
  }, [errandId, getToken]);

  useEffect(() => {
    void reload();
  }, [reload]);

  // A new errand sits in PENDING_RESERVATION until credit-service answers, and
  // the contract's own advice is to poll this endpoint (or wait for the
  // notification) until it becomes OPEN or REJECTED. Nothing else is polled.
  const pending =
    errand !== null &&
    errand.viewerRole !== "PUBLIC" &&
    errand.status === "PENDING_RESERVATION";
  useEffect(() => {
    if (!pending) return;
    const handle = setInterval(() => void reload(), 3000);
    return () => clearInterval(handle);
  }, [pending, reload]);

  // Every transition sends the version the user was shown. A stale version
  // means someone else moved first, so the errand is reloaded and the user
  // decides again rather than having their click replayed.
  async function run(
    label: string,
    call: (token: string, version: number) => Promise<ErrandView>,
  ) {
    if (!errand) return;
    setBusy(true);
    setError(null);
    setNotice(null);
    try {
      const token = await getToken();
      const updated = await call(token, errand.version);
      setErrand(updated);
      setNotice(label);
      onChanged();
    } catch (err) {
      setError(errandError(err));
      if (isStale(err)) await reload();
      onChanged();
    } finally {
      setBusy(false);
    }
  }

  async function submitReason() {
    if (!prompt || !errand) return;
    const problem = validateReason(reason);
    if (problem) {
      setReasonError(problem);
      return;
    }
    const text = reason.trim();
    const verb = prompt.verb;
    setPrompt(null);
    setReason("");
    setReasonError(null);
    await run(
      verb === "dispute" ? "Sent to an administrator for review." : "Problem reported.",
      (token, version) =>
        verb === "dispute"
          ? disputeErrand(token, errandId, version, text)
          : reportIssue(token, errandId, version, text),
    );
  }

  if (loading && !errand) {
    return (
      <>
        <BackLink onBack={onBack} />
        <p className="muted center mt">Loading errand…</p>
      </>
    );
  }

  if (!errand) {
    return (
      <>
        <BackLink onBack={onBack} />
        <div className="banner banner-error" role="alert">
          <span>{error ?? "This errand could not be loaded."}</span>
        </div>
      </>
    );
  }

  const isPublic = errand.viewerRole === "PUBLIC";
  // The open list and the public view have no status field: every errand there
  // is OPEN by definition.
  const status = isPublic ? "OPEN" : errand.status;
  const requester = errand.viewerRole === "REQUESTER" ? errand : null;
  const courier = errand.viewerRole === "COURIER" ? errand : null;

  return (
    <>
      <div className="page-head">
        <div>
          <BackLink onBack={onBack} />
          <h1 style={{ marginTop: "0.4rem" }}>{errand.pickup.name}</h1>
          <div className="head-meta">
            <span className={statusClass(status)}>{statusLabel(status)}</span>
            <span className="reward-chip">{creditText(errand.reward)}</span>
            {requester && <span className="type-chip">You requested this</span>}
            {courier && <span className="type-chip">You are delivering this</span>}
          </div>
        </div>
      </div>

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

      {requester?.rejectionReason && (
        <div className="banner banner-error" role="alert">
          <span>
            {REJECTION_TEXT[requester.rejectionReason] ?? requester.rejectionReason} Nothing was
            charged.
          </span>
        </div>
      )}

      {requester?.pendingEdit && (
        <div className="banner banner-wait" role="status">
          <span>
            A reward change to {creditText(requester.pendingEdit.reward)} is waiting for the credit
            service. Couriers still see the current details and can accept them. If the change is
            refused, this errand keeps its original reward.
          </span>
        </div>
      )}

      {!isPublic && errand.reviewReason && (
        <div className="banner banner-wait" role="status">
          <span>
            In review: {reviewReasonLabel(errand.reviewReason)}. An administrator decides whether
            the reward is transferred or refunded.
          </span>
        </div>
      )}

      <Actions
        status={status}
        role={errand.viewerRole}
        busy={busy}
        onAccept={() =>
          run("Accepted — it is yours to deliver.", (token, version) =>
            acceptErrand(token, errandId, version),
          )
        }
        onCancel={() =>
          run("Cancelled; your credits were released.", (token, version) =>
            cancelErrand(token, errandId, version),
          )
        }
        onConfirm={() =>
          run("Confirmed; the reward went to the courier.", (token, version) =>
            confirmErrand(token, errandId, version),
          )
        }
        onPickUp={() =>
          run("Marked as picked up.", (token, version) => pickUpErrand(token, errandId, version))
        }
        onDeliver={() =>
          run("Marked as delivered; waiting for the requester.", (token, version) =>
            deliverErrand(token, errandId, version),
          )
        }
        onWithdraw={() =>
          run("You withdrew from this errand.", (token, version) =>
            withdrawErrand(token, errandId, version),
          )
        }
        onEdit={requester ? () => onEdit(requester) : undefined}
        onOrderAgain={
          requester && TERMINAL_STATUSES.includes(requester.status)
            ? () => onOrderAgain(requester)
            : undefined
        }
        onDispute={() =>
          setPrompt({
            verb: "dispute",
            title: "Dispute this delivery",
            label: "What was wrong with it?",
          })
        }
        onReportIssue={() =>
          setPrompt({
            verb: "report-issue",
            title: "Report a problem",
            label: "What went wrong?",
          })
        }
      />

      <section className="panel">
        <h2>Items</h2>
        <ul className="item-list">
          {errand.items.map((item, index) => (
            <li key={index}>
              <span className="qty">{item.quantity}×</span>
              <span>{item.description}</span>
            </li>
          ))}
        </ul>
        <p className="muted small">
          {itemCount(errand.items)} in total · reward {creditText(errand.reward)}
        </p>
      </section>

      <section className="panel">
        <h2>Pickup</h2>
        <dl className="fields">
          <div>
            <dt>Supplier</dt>
            <dd>{errand.pickup.name}</dd>
          </div>
          <div>
            <dt>Where</dt>
            <dd>
              {errand.pickup.building} · Floor {errand.pickup.floor}
            </dd>
          </div>
          <div className="full">
            <dt>Directions</dt>
            <dd>{errand.pickup.locationDescription}</dd>
          </div>
          <div>
            <dt>Latitude</dt>
            <dd className="mono">{errand.pickup.latitude}</dd>
          </div>
          <div>
            <dt>Longitude</dt>
            <dd className="mono">{errand.pickup.longitude}</dd>
          </div>
        </dl>
        <p className="muted small">
          These details were saved when the errand was created, so they stay readable even if the
          supplier changes later.
        </p>
      </section>

      <section className="panel">
        <h2>Drop-off</h2>
        <dl className="fields">
          <div>
            <dt>Building</dt>
            <dd>{errand.deliveryBuilding.name}</dd>
          </div>
          <div className="full">
            <dt>Detail</dt>
            <dd>{errand.deliveryDetail}</dd>
          </div>
          {!isPublic && errand.notes && (
            <div className="full">
              <dt>Notes for the courier</dt>
              <dd>{errand.notes}</dd>
            </div>
          )}
        </dl>
      </section>

      {/* The requester sees the contact person they nominated; the courier sees
          it only while the errand is theirs to deliver. Contact is one-way. */}
      {(requester || (courier && courier.contactName)) && (
        <section className="panel">
          <h2>{requester ? "Contact you chose" : "Who to contact"}</h2>
          <dl className="fields">
            <div>
              <dt>Name</dt>
              <dd>{requester?.contactName ?? courier?.contactName}</dd>
            </div>
            <div>
              <dt>Mobile</dt>
              <dd className="mono">{requester?.contactMobile ?? courier?.contactMobile}</dd>
            </div>
          </dl>
        </section>
      )}

      <section className="panel">
        <h2>Timeline</h2>
        <dl className="fields">
          <div>
            <dt>Requested</dt>
            <dd>{formatDateTime(errand.createdAt)}</dd>
          </div>
          <div>
            <dt>Acceptance deadline</dt>
            <dd>
              {formatDateTime(errand.acceptanceDeadline)}
              <span className="muted"> · {timeLeft(errand.acceptanceDeadline)}</span>
            </dd>
          </div>
          {!isPublic && errand.pickupDeadline && (
            <div>
              <dt>Pickup deadline</dt>
              <dd>
                {formatDateTime(errand.pickupDeadline)}
                <span className="muted"> · {timeLeft(errand.pickupDeadline)}</span>
              </dd>
            </div>
          )}
          {!isPublic && errand.deliveryDeadline && (
            <div>
              <dt>Delivery deadline</dt>
              <dd>
                {formatDateTime(errand.deliveryDeadline)}
                <span className="muted"> · {timeLeft(errand.deliveryDeadline)}</span>
              </dd>
            </div>
          )}
          {!isPublic && errand.deliveredAt && (
            <div>
              <dt>Delivered</dt>
              <dd>{formatDateTime(errand.deliveredAt)}</dd>
            </div>
          )}
          {!isPublic && errand.verificationDeadline && (
            <div>
              <dt>Auto-completes</dt>
              <dd>
                {formatDateTime(errand.verificationDeadline)}
                <span className="muted"> · {timeLeft(errand.verificationDeadline)}</span>
              </dd>
            </div>
          )}
        </dl>
      </section>

      <section className="panel">
        <h2>Record</h2>
        <dl className="fields">
          <div className="full">
            <dt>Errand ID</dt>
            <dd className="mono">{errand.errandId}</dd>
          </div>
          <div>
            <dt>Version</dt>
            <dd className="mono">{errand.version}</dd>
          </div>
          {!isPublic && (
            <div>
              <dt>Last updated</dt>
              <dd>{formatDateTime(errand.updatedAt)}</dd>
            </div>
          )}
        </dl>
      </section>

      {prompt && (
        <div className="modal-backdrop" role="dialog" aria-modal="true" aria-label={prompt.title}>
          <div className="modal">
            <h2>{prompt.title}</h2>
            <p className="muted">
              An administrator reads this and decides whether the reward is transferred or
              refunded. The credits stay held until they do.
            </p>
            <div className="field">
              <label htmlFor="reason">{prompt.label}</label>
              <textarea
                id="reason"
                className="input"
                rows={4}
                value={reason}
                maxLength={LIMITS.reason}
                onChange={(e) => {
                  setReason(e.target.value);
                  setReasonError(null);
                }}
                aria-invalid={!!reasonError}
              />
              {reasonError && <p className="hint error">{reasonError}</p>}
              <p className="hint muted">
                {reason.trim().length}/{LIMITS.reason}
              </p>
            </div>
            <div className="actions">
              <button type="button" className="btn slim" onClick={submitReason} disabled={busy}>
                Submit
              </button>
              <button
                type="button"
                className="btn outline slim"
                onClick={() => {
                  setPrompt(null);
                  setReason("");
                  setReasonError(null);
                }}
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

function BackLink({ onBack }: { onBack: () => void }) {
  return (
    <button type="button" className="link-btn" onClick={onBack}>
      ← Back
    </button>
  );
}

// The buttons each state offers, following Diagram 1: the service rejects
// anything else, and these are only the transitions it can accept.
function Actions(props: {
  status: string;
  role: string;
  busy: boolean;
  onAccept: () => void;
  onCancel: () => void;
  onConfirm: () => void;
  onPickUp: () => void;
  onDeliver: () => void;
  onWithdraw: () => void;
  onEdit?: () => void;
  // Set only for the requester of a terminal errand.
  onOrderAgain?: () => void;
  onDispute: () => void;
  onReportIssue: () => void;
}) {
  const { status, role, busy } = props;
  const buttons: { label: string; onClick: () => void; kind?: string }[] = [];

  if (role === "PUBLIC" && status === "OPEN") {
    buttons.push({ label: "Accept errand", onClick: props.onAccept });
  }

  if (role === "REQUESTER") {
    if (status === "OPEN") {
      if (props.onEdit) buttons.push({ label: "Edit", onClick: props.onEdit, kind: "outline" });
      buttons.push({ label: "Cancel errand", onClick: props.onCancel, kind: "danger" });
    }
    if (status === "DELIVERED") {
      buttons.push({ label: "Confirm delivery", onClick: props.onConfirm });
      buttons.push({ label: "Dispute", onClick: props.onDispute, kind: "danger" });
    }
    if (props.onOrderAgain) buttons.push({ label: "Order again", onClick: props.onOrderAgain });
  }

  if (role === "COURIER") {
    if (status === "ASSIGNED") {
      buttons.push({ label: "Mark picked up", onClick: props.onPickUp });
      buttons.push({ label: "Withdraw", onClick: props.onWithdraw, kind: "danger" });
    }
    if (status === "PICKED_UP") {
      buttons.push({ label: "Mark delivered", onClick: props.onDeliver });
      buttons.push({ label: "Report a problem", onClick: props.onReportIssue, kind: "danger" });
    }
  }

  if (buttons.length === 0) return null;

  return (
    <div className="panel action-panel">
      <div className="actions" style={{ marginTop: 0 }}>
        {buttons.map((button) => (
          <button
            key={button.label}
            type="button"
            className={`btn slim${button.kind ? ` ${button.kind}` : ""}`}
            onClick={button.onClick}
            disabled={busy}
          >
            {busy ? "Working…" : button.label}
          </button>
        ))}
      </div>
      {role === "COURIER" && status === "ASSIGNED" && (
        <p className="muted small">
          Withdrawing counts towards the withdrawal restriction, and you cannot accept this errand
          again.
        </p>
      )}
    </div>
  );
}
