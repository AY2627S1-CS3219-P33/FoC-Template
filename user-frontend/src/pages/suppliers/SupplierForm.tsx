import { useMemo, useState, type FormEvent, type ReactNode } from "react";
import { SupplierError, type Supplier, type SupplierWrite } from "../../api/supplier";
import {
  LIMITS,
  hasErrors,
  validateSupplierForm,
  type SupplierFormErrors,
} from "../../supplierValidation";

interface Props {
  mode: "create" | "edit";
  initial?: Supplier;
  onSubmit: (body: SupplierWrite) => Promise<void>;
  onCancel: () => void;
}

interface Draft {
  name: string;
  type: string;
  building: string;
  floor: string;
  locationDescription: string;
  latitude: string;
  longitude: string;
  openingTime: string;
  closingTime: string;
  imageUrl: string;
}

function draftFrom(initial?: Supplier): Draft {
  return {
    name: initial?.name ?? "",
    type: initial?.type ?? "",
    building: initial?.building ?? "",
    floor: initial?.floor ?? "",
    locationDescription: initial?.locationDescription ?? "",
    latitude: initial ? String(initial.latitude) : "",
    longitude: initial ? String(initial.longitude) : "",
    openingTime: initial?.openingTime ?? "",
    closingTime: initial?.closingTime ?? "",
    imageUrl: initial?.imageUrl ?? "",
  };
}

function toBody(draft: Draft): SupplierWrite {
  return {
    name: draft.name.trim(),
    type: draft.type.trim(),
    building: draft.building.trim(),
    floor: draft.floor.trim(),
    locationDescription: draft.locationDescription.trim(),
    latitude: Number(draft.latitude),
    longitude: Number(draft.longitude),
    openingTime: draft.openingTime,
    closingTime: draft.closingTime,
    imageUrl: draft.imageUrl.trim() === "" ? null : draft.imageUrl.trim(),
  };
}

export function SupplierForm({ mode, initial, onSubmit, onCancel }: Props) {
  const original = useMemo(() => draftFrom(initial), [initial]);
  const [draft, setDraft] = useState<Draft>(original);
  const [submitted, setSubmitted] = useState(false);
  const [saving, setSaving] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  const [serverFieldErrors, setServerFieldErrors] = useState<SupplierFormErrors>({});
  const [confirmCancel, setConfirmCancel] = useState(false);

  const clientErrors = useMemo(() => validateSupplierForm(draft), [draft]);
  const errors: SupplierFormErrors = { ...clientErrors, ...serverFieldErrors };

  const dirty = useMemo(
    () => (Object.keys(draft) as (keyof Draft)[]).some((k) => draft[k] !== original[k]),
    [draft, original],
  );

  function set<K extends keyof Draft>(key: K, value: string) {
    setDraft((d) => ({ ...d, [key]: value }));
    if (serverFieldErrors[key as keyof SupplierFormErrors]) {
      setServerFieldErrors((e) => ({ ...e, [key]: undefined }));
    }
  }

  async function onFormSubmit(event: FormEvent) {
    event.preventDefault();
    setSubmitted(true);
    setFormError(null);
    if (hasErrors(clientErrors)) return;
    setSaving(true);
    try {
      await onSubmit(toBody(draft));
    } catch (err) {
      setSaving(false);
      if (err instanceof SupplierError) {
        if (err.code === "SUPPLIER_NAME_CONFLICT") {
          setServerFieldErrors({ name: "Another supplier already uses this name." });
        } else if (err.fields.length > 0) {
          const mapped: SupplierFormErrors = {};
          for (const f of err.fields) mapped[f.field as keyof SupplierFormErrors] = f.message;
          setServerFieldErrors(mapped);
        } else {
          setFormError(err.message);
        }
      } else {
        setFormError("Could not save this supplier.");
      }
    }
  }

  function requestCancel() {
    if (dirty) setConfirmCancel(true);
    else onCancel();
  }

  const show = (field: keyof SupplierFormErrors) =>
    (submitted || serverFieldErrors[field]) && errors[field] ? errors[field] : "";

  return (
    <>
      <div className="page-head">
        <div>
          <button type="button" className="link-btn" onClick={requestCancel}>
            ← Cancel
          </button>
          <h1 style={{ marginTop: "0.4rem" }}>
            {mode === "create" ? "New supplier" : `Edit ${initial?.name ?? "supplier"}`}
          </h1>
        </div>
      </div>

      {formError && (
        <div className="banner banner-error" role="alert">
          <span>{formError}</span>
        </div>
      )}

      <form className="panel" onSubmit={onFormSubmit} noValidate>
        <Field label="Name" error={show("name")}>
          <input className="input" value={draft.name} maxLength={LIMITS.name + 20}
            onChange={(e) => set("name", e.target.value)} aria-invalid={!!show("name")} />
        </Field>

        <Field label="Type" error={show("type")}>
          <input className="input" value={draft.type} maxLength={LIMITS.type + 20}
            placeholder="e.g. Food, Shopping, Printing"
            onChange={(e) => set("type", e.target.value)} aria-invalid={!!show("type")} />
        </Field>

        <div className="form-grid">
          <Field label="Building" error={show("building")}>
            <input className="input" value={draft.building} maxLength={LIMITS.building + 20}
              onChange={(e) => set("building", e.target.value)} aria-invalid={!!show("building")} />
          </Field>
          <Field label="Floor" error={show("floor")}>
            <input className="input" value={draft.floor} maxLength={LIMITS.floor + 20}
              onChange={(e) => set("floor", e.target.value)} aria-invalid={!!show("floor")} />
          </Field>
        </div>

        <Field label="Location description" error={show("locationDescription")}>
          <textarea className="input" rows={2} value={draft.locationDescription}
            maxLength={LIMITS.locationDescription + 40}
            onChange={(e) => set("locationDescription", e.target.value)}
            aria-invalid={!!show("locationDescription")} />
        </Field>

        <div className="form-grid">
          <Field label="Latitude" error={show("latitude")}>
            <input className="input" value={draft.latitude} inputMode="decimal"
              placeholder="1.2963" onChange={(e) => set("latitude", e.target.value)}
              aria-invalid={!!show("latitude")} />
          </Field>
          <Field label="Longitude" error={show("longitude")}>
            <input className="input" value={draft.longitude} inputMode="decimal"
              placeholder="103.7730" onChange={(e) => set("longitude", e.target.value)}
              aria-invalid={!!show("longitude")} />
          </Field>
        </div>

        <div className="form-grid">
          <Field label="Opening time" error={show("openingTime")}>
            <input className="input" type="time" value={draft.openingTime}
              onChange={(e) => set("openingTime", e.target.value)} aria-invalid={!!show("openingTime")} />
          </Field>
          <Field label="Closing time" error={show("closingTime")}>
            <input className="input" type="time" value={draft.closingTime}
              onChange={(e) => set("closingTime", e.target.value)} aria-invalid={!!show("closingTime")} />
          </Field>
        </div>

        <Field label="Image URL (optional)" error={show("imageUrl")}>
          <input className="input" value={draft.imageUrl} maxLength={LIMITS.imageUrl + 20}
            placeholder="https://…" onChange={(e) => set("imageUrl", e.target.value)}
            aria-invalid={!!show("imageUrl")} />
        </Field>

        <div className="actions">
          <button type="submit" className="btn slim" disabled={saving || (mode === "edit" && !dirty)}>
            {saving ? "Saving…" : mode === "create" ? "Create supplier" : "Save changes"}
          </button>
          <button type="button" className="btn outline slim" onClick={requestCancel} disabled={saving}>
            Cancel
          </button>
        </div>
      </form>

      {confirmCancel && (
        <div className="modal-backdrop" role="dialog" aria-modal="true" aria-label="Discard changes">
          <div className="modal">
            <h2>Discard changes?</h2>
            <p className="muted">All progress on this form will be lost.</p>
            <div className="actions">
              <button type="button" className="btn danger slim" onClick={onCancel}>
                Discard
              </button>
              <button type="button" className="btn outline slim" onClick={() => setConfirmCancel(false)}>
                Keep editing
              </button>
            </div>
          </div>
        </div>
      )}
    </>
  );
}

function Field({
  label,
  error,
  children,
}: {
  label: string;
  error: string | undefined;
  children: ReactNode;
}) {
  return (
    <div className="field">
      <label>{label}</label>
      {children}
      {error && <p className="hint error">{error}</p>}
    </div>
  );
}
