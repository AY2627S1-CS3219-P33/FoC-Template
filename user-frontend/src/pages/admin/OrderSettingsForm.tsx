import { useEffect, useState, type FormEvent } from "react";
import { getSettings, putSettings, OrderError, type Settings } from "../../api/order";
import { errandError } from "../errands/messages";
import { formatDuration } from "../errands/format";

interface Props {
  getToken: () => Promise<string>;
}

// Every value is a whole number of seconds and must be positive. PUT replaces
// the complete object, so the form always sends every field — editing one and
// omitting the rest would reset them to defaults.
type Draft = Record<string, string>;

const DURATIONS: { key: keyof Settings & string; label: string; hint: string }[] = [
  {
    key: "pickupAfter",
    label: "Pickup deadline",
    hint: "After a courier accepts, how long they have to pick the items up.",
  },
  {
    key: "deliveryAfter",
    label: "Delivery deadline",
    hint: "After a courier accepts, how long they have to deliver.",
  },
  {
    key: "verificationAfter",
    label: "Auto-complete after delivery",
    hint: "An unconfirmed delivery completes itself after this long.",
  },
  {
    key: "verificationReminderAfter",
    label: "Verification reminder",
    hint: "When the requester is reminded to confirm. Must be less than the auto-complete time.",
  },
  {
    key: "reservationTimeout",
    label: "Stuck reservation timeout",
    hint: "How long the reconciler waits for a reservation result before giving up.",
  },
  {
    key: "editTimeout",
    label: "Stuck edit timeout",
    hint: "How long a pending reward edit waits for the credit service.",
  },
  {
    key: "outboxConfirmTimeout",
    label: "Outbox confirm timeout",
    hint: "A sent credit command with no result is re-sent after this long.",
  },
];

function draftFrom(settings: Settings): Draft {
  return {
    pickupAfter: String(settings.pickupAfter),
    deliveryAfter: String(settings.deliveryAfter),
    verificationAfter: String(settings.verificationAfter),
    verificationReminderAfter: String(settings.verificationReminderAfter),
    reservationTimeout: String(settings.reservationTimeout),
    editTimeout: String(settings.editTimeout),
    outboxConfirmTimeout: String(settings.outboxConfirmTimeout),
    withdrawalCount: String(settings.withdrawalRestriction.count),
    withdrawalWindow: String(settings.withdrawalRestriction.window),
    withdrawalDuration: String(settings.withdrawalRestriction.duration),
    cancellationCount: String(settings.cancellationFlag.count),
    cancellationWindow: String(settings.cancellationFlag.window),
  };
}

function positiveInt(value: string): number | null {
  const trimmed = value.trim();
  if (!/^\d+$/.test(trimmed)) return null;
  const parsed = Number(trimmed);
  return parsed >= 1 ? parsed : null;
}

export function OrderSettingsForm({ getToken }: Props) {
  const [draft, setDraft] = useState<Draft | null>(null);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const token = await getToken();
        const settings = await getSettings(token);
        if (!cancelled) setDraft(draftFrom(settings));
      } catch (err) {
        if (!cancelled) setError(errandError(err));
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [getToken]);

  function set(key: string, value: string) {
    setDraft((d) => (d ? { ...d, [key]: value } : d));
    setFieldErrors((e) => {
      if (!e[key]) return e;
      const next = { ...e };
      delete next[key];
      return next;
    });
  }

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    if (!draft) return;
    setNotice(null);
    setError(null);

    const problems: Record<string, string> = {};
    const values: Record<string, number> = {};
    for (const [key, raw] of Object.entries(draft)) {
      const parsed = positiveInt(raw);
      if (parsed === null) problems[key] = "Must be a whole number of at least 1.";
      else values[key] = parsed;
    }
    // The service enforces this too; catching it here avoids a round trip.
    if (
      problems.verificationReminderAfter === undefined &&
      problems.verificationAfter === undefined &&
      values.verificationReminderAfter >= values.verificationAfter
    ) {
      problems.verificationReminderAfter = "Must be less than the auto-complete time.";
    }
    if (Object.keys(problems).length > 0) {
      setFieldErrors(problems);
      setError("Please correct the highlighted fields.");
      return;
    }

    const settings: Settings = {
      pickupAfter: values.pickupAfter,
      deliveryAfter: values.deliveryAfter,
      verificationAfter: values.verificationAfter,
      verificationReminderAfter: values.verificationReminderAfter,
      reservationTimeout: values.reservationTimeout,
      editTimeout: values.editTimeout,
      outboxConfirmTimeout: values.outboxConfirmTimeout,
      withdrawalRestriction: {
        count: values.withdrawalCount,
        window: values.withdrawalWindow,
        duration: values.withdrawalDuration,
      },
      cancellationFlag: {
        count: values.cancellationCount,
        window: values.cancellationWindow,
      },
    };

    setSaving(true);
    try {
      const token = await getToken();
      const saved = await putSettings(token, settings);
      setDraft(draftFrom(saved));
      setNotice("Settings saved. New deadlines apply to transitions from now on.");
    } catch (err) {
      if (err instanceof OrderError && err.fields.length > 0) {
        const mapped: Record<string, string> = {};
        for (const field of err.fields) mapped[field.field] = field.message;
        setFieldErrors(mapped);
      }
      setError(errandError(err));
    } finally {
      setSaving(false);
    }
  }

  if (loading) return <p className="muted center mt">Loading settings…</p>;

  if (!draft) {
    return (
      <div className="banner banner-error" role="alert">
        <span>{error ?? "Settings could not be loaded."}</span>
      </div>
    );
  }

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

      <form onSubmit={onSubmit} noValidate>
        <section className="panel">
          <h2>Deadlines and timeouts</h2>
          <p className="muted small">
            All values are in seconds. Changing a deadline does not recompute deadlines already set
            on errands in flight.
          </p>
          <div className="form-grid mt">
            {DURATIONS.map((field) => (
              <div className="field" key={field.key}>
                <label htmlFor={field.key}>{field.label}</label>
                <div className="input-affix">
                  <input
                    id={field.key}
                    className="input"
                    value={draft[field.key]}
                    inputMode="numeric"
                    onChange={(e) => set(field.key, e.target.value)}
                    aria-invalid={!!fieldErrors[field.key]}
                  />
                  <span className="suffix">
                    {positiveInt(draft[field.key]) !== null
                      ? formatDuration(Number(draft[field.key]))
                      : "s"}
                  </span>
                </div>
                <p className="hint muted">{field.hint}</p>
                {fieldErrors[field.key] && <p className="hint error">{fieldErrors[field.key]}</p>}
              </div>
            ))}
          </div>
        </section>

        <section className="panel">
          <h2>Withdrawal restriction</h2>
          <p className="muted small">
            A courier who withdraws this many times inside the window — voluntarily or by missing a
            deadline — cannot accept errands for the restriction duration.
          </p>
          <div className="form-grid mt">
            <Number3
              id="withdrawalCount"
              label="Withdrawals"
              value={draft.withdrawalCount}
              error={fieldErrors.withdrawalCount}
              onChange={(v) => set("withdrawalCount", v)}
            />
            <Number3
              id="withdrawalWindow"
              label="Within (seconds)"
              value={draft.withdrawalWindow}
              error={fieldErrors.withdrawalWindow}
              onChange={(v) => set("withdrawalWindow", v)}
            />
            <Number3
              id="withdrawalDuration"
              label="Restriction (seconds)"
              value={draft.withdrawalDuration}
              error={fieldErrors.withdrawalDuration}
              onChange={(v) => set("withdrawalDuration", v)}
            />
          </div>
        </section>

        <section className="panel">
          <h2>Cancellation flag</h2>
          <p className="muted small">
            A requester who cancels more than this many times inside the window is flagged for an
            administrator to look at. Flagging does not block them.
          </p>
          <div className="form-grid mt">
            <Number3
              id="cancellationCount"
              label="Cancellations"
              value={draft.cancellationCount}
              error={fieldErrors.cancellationCount}
              onChange={(v) => set("cancellationCount", v)}
            />
            <Number3
              id="cancellationWindow"
              label="Within (seconds)"
              value={draft.cancellationWindow}
              error={fieldErrors.cancellationWindow}
              onChange={(v) => set("cancellationWindow", v)}
            />
          </div>
        </section>

        <div className="actions">
          <button type="submit" className="btn slim" disabled={saving}>
            {saving ? "Saving…" : "Save all settings"}
          </button>
        </div>
      </form>
    </>
  );
}

function Number3(props: {
  id: string;
  label: string;
  value: string;
  error?: string;
  onChange: (value: string) => void;
}) {
  return (
    <div className="field">
      <label htmlFor={props.id}>{props.label}</label>
      <input
        id={props.id}
        className="input"
        value={props.value}
        inputMode="numeric"
        onChange={(e) => props.onChange(e.target.value)}
        aria-invalid={!!props.error}
      />
      {props.error && <p className="hint error">{props.error}</p>}
    </div>
  );
}
