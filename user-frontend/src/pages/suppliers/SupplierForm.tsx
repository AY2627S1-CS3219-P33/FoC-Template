// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: User-frontend suppliers page and SupplierForm behavior.
// Author review: I validated correctness, edited for style, and added boundary checks.

import { useMemo, useRef, useState, type ChangeEvent, type FormEvent, type ReactNode } from "react";
import {
  SupplierError,
  type Lookup,
  type Supplier,
  type SupplierWrite,
} from "../../api/supplier";
import {
  LIMITS,
  hasErrors,
  validateSupplierForm,
  type SupplierFormErrors,
} from "../../supplierValidation";

interface Props {
  mode: "create" | "edit";
  initial?: Supplier;
  // Managed lists, retired rows included so an existing supplier's current
  // building or type still renders.
  types: Lookup[];
  buildings: Lookup[];
  onSubmit: (body: SupplierWrite) => Promise<void>;
  onUpload: (file: File) => Promise<string>;
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
    // These are lookup names chosen from a list, so they are already canonical.
    type: draft.type,
    building: draft.building,
    floor: draft.floor.trim(),
    locationDescription: draft.locationDescription.trim(),
    latitude: Number(draft.latitude),
    longitude: Number(draft.longitude),
    openingTime: draft.openingTime,
    closingTime: draft.closingTime,
    imageUrl: draft.imageUrl.trim() === "" ? null : draft.imageUrl.trim(),
  };
}

export function SupplierForm({ mode, initial, types, buildings, onSubmit, onUpload, onCancel }: Props) {
  const original = useMemo(() => draftFrom(initial), [initial]);
  const [draft, setDraft] = useState<Draft>(original);
  const [submitted, setSubmitted] = useState(false);
  const [saving, setSaving] = useState(false);
  const [uploading, setUploading] = useState(false);
  const [imageError, setImageError] = useState<string | null>(null);
  const [formError, setFormError] = useState<string | null>(null);
  const [serverFieldErrors, setServerFieldErrors] = useState<SupplierFormErrors>({});
  const [confirmCancel, setConfirmCancel] = useState(false);
  const imageInput = useRef<HTMLInputElement>(null);

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
    if (saving || uploading || imageError) return;
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
        } else if (err.code === "BUILDING_NOT_FOUND") {
          setServerFieldErrors({
            building: "This building no longer exists or has been retired. Pick another.",
          });
        } else if (err.code === "SUPPLIER_TYPE_NOT_FOUND") {
          setServerFieldErrors({
            type: "This type no longer exists or has been retired. Pick another.",
          });
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
    if (saving || uploading) return;
    if (dirty) setConfirmCancel(true);
    else onCancel();
  }

  async function onImageSelected(event: ChangeEvent<HTMLInputElement>) {
    const file = event.currentTarget.files?.[0];
    event.currentTarget.value = "";
    if (!file) return;
    setImageError(null);
    if (file.size > 5 * 1024 * 1024) {
      setImageError("Choose an image smaller than or equal to 5 MiB.");
      return;
    }
    if (file.type !== "image/jpeg" && file.type !== "image/png") {
      setImageError("Choose a JPEG or PNG image.");
      return;
    }
    setUploading(true);
    try {
      set("imageUrl", await onUpload(file));
    } catch (error) {
      setImageError(error instanceof SupplierError ? error.message : "Could not upload the image. Please try again.");
    } finally {
      setUploading(false);
    }
  }

  const show = (field: keyof SupplierFormErrors) =>
    (submitted || serverFieldErrors[field]) && errors[field] ? errors[field] : "";

  // Only a live row can be assigned; the one this supplier already uses stays
  // selectable so an unrelated edit does not force a change.
  function options(lookups: Lookup[], current: string) {
    const live = lookups.filter((item) => !item.retired);
    if (current !== "" && !live.some((item) => item.name === current)) {
      const retired = lookups.find((item) => item.name === current);
      if (retired) return [retired, ...live];
    }
    return live;
  }

  const typeOptions = options(types, original.type);
  const buildingOptions = options(buildings, original.building);
  const missingLookups = typeOptions.length === 0 || buildingOptions.length === 0;

  return (
    <>
      <div className="page-head">
        <div>
          <button type="button" className="link-btn" onClick={requestCancel} disabled={saving || uploading}>
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

      {missingLookups && (
        <div className="banner banner-error" role="alert">
          <span>
            {buildingOptions.length === 0 && typeOptions.length === 0
              ? "No buildings or supplier types exist yet."
              : buildingOptions.length === 0
                ? "No buildings exist yet."
                : "No supplier types exist yet."}{" "}
            Add them under Buildings &amp; types before creating a supplier.
          </span>
        </div>
      )}

      <form className="panel" onSubmit={onFormSubmit} noValidate>
        <Field label="Name" error={show("name")}>
          <input className="input" value={draft.name} maxLength={LIMITS.name}
            onChange={(e) => set("name", e.target.value)} aria-invalid={!!show("name")} />
        </Field>

        <Field label="Type" error={show("type")}>
          <select className="input select" value={draft.type}
            onChange={(e) => set("type", e.target.value)} aria-invalid={!!show("type")}>
            <option value="">Choose a type…</option>
            {typeOptions.map((item) => (
              <option key={item.id} value={item.name}>
                {item.name}
                {item.retired ? " (retired)" : ""}
              </option>
            ))}
          </select>
        </Field>

        <div className="form-grid">
          <Field label="Building" error={show("building")}>
            <select className="input select" value={draft.building}
              onChange={(e) => set("building", e.target.value)} aria-invalid={!!show("building")}>
              <option value="">Choose a building…</option>
              {buildingOptions.map((item) => (
                <option key={item.id} value={item.name}>
                  {item.name}
                  {item.retired ? " (retired)" : ""}
                </option>
              ))}
            </select>
          </Field>
          <Field label="Floor" error={show("floor")}>
            <input className="input" value={draft.floor} maxLength={LIMITS.floor}
              onChange={(e) => set("floor", e.target.value)} aria-invalid={!!show("floor")} />
          </Field>
        </div>
        <p className="hint muted">
          Buildings and types are managed lists, not free text. Add a missing one under Buildings
          &amp; types.
        </p>

        <Field label="Location description" error={show("locationDescription")}>
          <textarea className="input" rows={2} value={draft.locationDescription}
            maxLength={LIMITS.locationDescription}
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

        <fieldset className="supplier-image-field">
          <legend>Supplier image <span className="muted">(optional)</span></legend>
          <div className="supplier-image-card">
            <div className="supplier-image-preview">
              {draft.imageUrl && !errors.imageUrl ? (
                <img src={draft.imageUrl} alt="Supplier image preview" />
              ) : (
                <>
                  <svg width="36" height="36" viewBox="0 0 24 24" fill="none" stroke="currentColor"
                    strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                    <rect x="3" y="3" width="18" height="18" rx="3" />
                    <circle cx="8.5" cy="8.5" r="1.5" />
                    <path d="m21 15-5-5L5 21" />
                  </svg>
                  <span>No image selected</span>
                </>
              )}
            </div>
            <div className="supplier-image-content">
              <p className="supplier-image-title">Add a photo or logo</p>
              <p id="supplier-image-help" className="hint muted">JPEG or PNG · Up to 5 MiB · 16 million pixels</p>
              <input ref={imageInput} id="supplier-image" type="file" accept="image/jpeg,image/png" hidden
                onChange={onImageSelected} disabled={saving || uploading} />
              <div className="supplier-image-actions">
                <button type="button" className="btn outline slim" disabled={saving || uploading}
                  aria-describedby="supplier-image-help supplier-image-error"
                  onClick={() => imageInput.current?.click()}>
                  {uploading ? "Uploading…" : draft.imageUrl ? "Replace image" : "Choose image"}
                </button>
                {(draft.imageUrl || imageError) && (
                  <button type="button" className="btn outline slim supplier-image-remove" disabled={saving || uploading}
                    onClick={() => { set("imageUrl", ""); setImageError(null); }}>Remove image</button>
                )}
              </div>
              <p className="supplier-image-status hint muted" role="status">
                {uploading ? "Uploading your image. Please wait…" : "Save the supplier to apply image changes."}
              </p>
              {uploading && <progress className="supplier-image-progress" aria-label="Image upload in progress" />}
            </div>
          </div>
          <p id="supplier-image-error" className="hint error" role="alert">{imageError || show("imageUrl")}</p>
          <details className="supplier-image-url">
            <summary>Use an image URL instead</summary>
            <label htmlFor="supplier-image-url" className="sr-only">Image URL</label>
            <input id="supplier-image-url" className="input" value={draft.imageUrl} maxLength={LIMITS.imageUrl}
              placeholder="https://…" disabled={saving || uploading}
              onChange={(e) => { set("imageUrl", e.target.value); setImageError(null); }}
              aria-describedby="supplier-image-error" aria-invalid={!!show("imageUrl")} />
          </details>
        </fieldset>

        <div className="actions">
          <button type="submit" className="btn slim"
            disabled={saving || uploading || !!imageError || missingLookups || (mode === "edit" && !dirty)}>
            {saving ? "Saving…" : mode === "create" ? "Create supplier" : "Save changes"}
          </button>
          <button type="button" className="btn outline slim" onClick={requestCancel} disabled={saving || uploading}>
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
