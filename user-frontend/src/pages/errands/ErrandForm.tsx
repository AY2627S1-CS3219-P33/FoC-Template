import { useEffect, useMemo, useState, type FormEvent, type ReactNode } from "react";
import { OrderError, type ErrandCreate, type ErrandEdit, type Item, type RequesterErrandView } from "../../api/order";
import {
  getSupplier,
  listSuppliers,
  SupplierError,
  type Lookup,
  type Supplier,
} from "../../api/supplier";
import type { Profile } from "../../api/types";
import {
  LIMITS,
  defaultDeadline,
  fromLocalInput,
  hasErrandErrors,
  toLocalInput,
  validateErrandForm,
  type ErrandDraft,
  type ErrandFormErrors,
  type ItemDraft,
} from "../../orderValidation";
import { creditText, formatDateTime } from "./format";

interface Props {
  mode: "create" | "edit";
  initial?: RequesterErrandView;
  // Create mode only: a finished errand to copy ("Order again").
  template?: RequesterErrandView;
  profile: Profile;
  buildings: Lookup[];
  getSupplierToken: () => Promise<string>;
  onSubmit: (body: ErrandCreate | ErrandEdit) => Promise<void>;
  onCancel: () => void;
}

// An edit keeps the errand's acceptance deadline; a copy for "Order again"
// starts from the default, since the old one has passed.
function draftFrom(
  initial: RequesterErrandView | undefined,
  profile: Profile,
  keepDeadline: boolean,
): ErrandDraft {
  if (!initial) {
    return {
      supplierId: "",
      deliveryBuildingId: "",
      deliveryDetail: "",
      items: [{ description: "", quantity: "1" }],
      notes: "",
      // Prefilled from the profile, but the requester may nominate someone else
      // as the contact (NFR2.6.1).
      contactName: profile.display_name || profile.username,
      contactMobile: profile.mobile_number,
      reward: "",
      acceptanceDeadline: defaultDeadline(),
    };
  }
  return {
    supplierId: initial.supplierId,
    deliveryBuildingId: initial.deliveryBuilding.id,
    deliveryDetail: initial.deliveryDetail,
    items: initial.items.map((item) => ({
      description: item.description,
      quantity: String(item.quantity),
    })),
    notes: initial.notes,
    contactName: initial.contactName,
    contactMobile: initial.contactMobile,
    reward: String(initial.reward),
    acceptanceDeadline: keepDeadline
      ? toLocalInput(new Date(initial.acceptanceDeadline))
      : defaultDeadline(),
  };
}

function toItems(drafts: ItemDraft[]): Item[] {
  return drafts.map((item) => ({
    description: item.description.trim(),
    quantity: Number(item.quantity),
  }));
}

export function ErrandForm({
  mode,
  initial,
  template,
  profile,
  buildings,
  getSupplierToken,
  onSubmit,
  onCancel,
}: Props) {
  // The errand the form starts from: the one being edited, or the one being
  // ordered again.
  const source = mode === "edit" ? initial : template;
  // State rather than a memo: "Order again" may drop a copied supplier or
  // building, and the baseline drops it too so the form still starts clean.
  const [original, setOriginal] = useState<ErrandDraft>(() =>
    draftFrom(source, profile, mode === "edit"),
  );
  const [draft, setDraft] = useState<ErrandDraft>(original);
  // A copy is shown only once its supplier has been checked.
  const [ready, setReady] = useState(!template);
  const [dropped, setDropped] = useState({ supplier: false, building: false });
  const [submitted, setSubmitted] = useState(false);
  const [saving, setSaving] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  const [serverFieldErrors, setServerFieldErrors] = useState<ErrandFormErrors>({});
  const [confirmCancel, setConfirmCancel] = useState(false);

  // Supplier picker. The search is the supplier service's own, so the picker
  // matches what the Suppliers screen would show for the same text.
  const [supplierQuery, setSupplierQuery] = useState("");
  const [suppliers, setSuppliers] = useState<Supplier[]>([]);
  const [supplierError, setSupplierError] = useState<string | null>(null);
  const [chosen, setChosen] = useState<Supplier | null>(null);

  useEffect(() => {
    let cancelled = false;
    const handle = setTimeout(async () => {
      try {
        const token = await getSupplierToken();
        const page = await listSuppliers(token, {
          q: supplierQuery || undefined,
          sort: "name",
          limit: 25,
        });
        if (cancelled) return;
        setSuppliers(page.items);
        setSupplierError(null);
      } catch {
        if (!cancelled) setSupplierError("Could not load pickup locations.");
      }
    }, 250);
    return () => {
      cancelled = true;
      clearTimeout(handle);
    };
  }, [supplierQuery, getSupplierToken]);

  // Keep the chosen supplier's summary visible even when it is not in the
  // current search results.
  useEffect(() => {
    if (draft.supplierId === "") {
      setChosen(null);
      return;
    }
    const match = suppliers.find((s) => s.supplierId === draft.supplierId);
    if (match) setChosen(match);
  }, [draft.supplierId, suppliers]);

  function drop(field: "supplierId" | "deliveryBuildingId", id: string) {
    const strip = (d: ErrandDraft) => (d[field] === id ? { ...d, [field]: "" } : d);
    setOriginal(strip);
    setDraft(strip);
  }

  // "Order again": a deleted supplier is 404 here. Any other failure keeps the
  // copy, and the create request validates it.
  useEffect(() => {
    if (!template) return;
    let cancelled = false;
    (async () => {
      let gone = false;
      try {
        const token = await getSupplierToken();
        const supplier = await getSupplier(token, template.supplierId);
        if (cancelled) return;
        if (supplier.available) setChosen(supplier);
        else gone = true;
      } catch (err) {
        gone = err instanceof SupplierError && err.status === 404;
      }
      if (cancelled) return;
      if (gone) {
        drop("supplierId", template.supplierId);
        setDropped((d) => ({ ...d, supplier: true }));
      }
      setReady(true);
    })();
    return () => {
      cancelled = true;
    };
  }, [template, getSupplierToken]);

  // "Order again": the copied building must still be live. The list arrives
  // from the page; until it has (or if it failed) the building is kept, and
  // the create request validates it.
  useEffect(() => {
    if (!template || buildings.length === 0) return;
    const id = template.deliveryBuilding.id;
    if (buildings.some((b) => b.id === id && !b.retired)) return;
    drop("deliveryBuildingId", id);
    setDropped((d) => ({ ...d, building: true }));
  }, [template, buildings]);

  // An errand being edited or copied already names a supplier, which the first
  // (empty) search need not contain. Until it is found, label it from the
  // pickup details stored on the errand so the picker never looks empty.
  const selectedLabel =
    chosen && chosen.supplierId === draft.supplierId
      ? `${chosen.name} — ${chosen.building}`
      : source && source.supplierId === draft.supplierId
        ? `${source.pickup.name} — ${source.pickup.building}`
        : null;
  const needsFallbackOption =
    draft.supplierId !== "" &&
    selectedLabel !== null &&
    !suppliers.some((s) => s.supplierId === draft.supplierId);

  const clientErrors = useMemo(() => validateErrandForm(draft), [draft]);
  const errors: ErrandFormErrors = { ...clientErrors, ...serverFieldErrors };

  const rewardChanged = mode === "edit" && draft.reward !== original.reward;
  const dirty =
    draft.supplierId !== original.supplierId ||
    draft.deliveryBuildingId !== original.deliveryBuildingId ||
    draft.deliveryDetail !== original.deliveryDetail ||
    draft.notes !== original.notes ||
    draft.contactName !== original.contactName ||
    draft.contactMobile !== original.contactMobile ||
    draft.reward !== original.reward ||
    draft.acceptanceDeadline !== original.acceptanceDeadline ||
    JSON.stringify(draft.items) !== JSON.stringify(original.items);

  function set<K extends keyof ErrandDraft>(key: K, value: ErrandDraft[K]) {
    setDraft((d) => ({ ...d, [key]: value }));
    if (serverFieldErrors[key as keyof ErrandFormErrors]) {
      setServerFieldErrors((e) => ({ ...e, [key]: undefined }));
    }
  }

  function setItem(index: number, patch: Partial<ItemDraft>) {
    setDraft((d) => ({
      ...d,
      items: d.items.map((item, i) => (i === index ? { ...item, ...patch } : item)),
    }));
  }

  function addItem() {
    if (draft.items.length >= LIMITS.items) return;
    setDraft((d) => ({ ...d, items: [...d.items, { description: "", quantity: "1" }] }));
  }

  function removeItem(index: number) {
    setDraft((d) => ({ ...d, items: d.items.filter((_, i) => i !== index) }));
  }

  // An edit sends expectedVersion plus only what changed; omitted fields keep
  // their current value.
  function editBody(): ErrandEdit {
    const body: ErrandEdit = { expectedVersion: initial!.version };
    if (draft.supplierId !== original.supplierId) body.supplierId = draft.supplierId;
    if (draft.deliveryBuildingId !== original.deliveryBuildingId)
      body.deliveryBuildingId = draft.deliveryBuildingId;
    if (draft.deliveryDetail !== original.deliveryDetail)
      body.deliveryDetail = draft.deliveryDetail.trim();
    if (JSON.stringify(draft.items) !== JSON.stringify(original.items))
      body.items = toItems(draft.items);
    if (draft.notes !== original.notes) body.notes = draft.notes.trim();
    if (draft.contactName !== original.contactName) body.contactName = draft.contactName.trim();
    if (draft.contactMobile !== original.contactMobile)
      body.contactMobile = draft.contactMobile.trim();
    if (draft.reward !== original.reward) body.reward = Number(draft.reward);
    if (draft.acceptanceDeadline !== original.acceptanceDeadline)
      body.acceptanceDeadline = fromLocalInput(draft.acceptanceDeadline);
    return body;
  }

  function createBody(): ErrandCreate {
    const body: ErrandCreate = {
      supplierId: draft.supplierId,
      deliveryBuildingId: draft.deliveryBuildingId,
      deliveryDetail: draft.deliveryDetail.trim(),
      items: toItems(draft.items),
      contactName: draft.contactName.trim(),
      contactMobile: draft.contactMobile.trim(),
      reward: Number(draft.reward),
    };
    const notes = draft.notes.trim();
    if (notes !== "") body.notes = notes;
    const deadline = fromLocalInput(draft.acceptanceDeadline);
    if (deadline) body.acceptanceDeadline = deadline;
    return body;
  }

  async function onFormSubmit(event: FormEvent) {
    event.preventDefault();
    setSubmitted(true);
    setFormError(null);
    if (hasErrandErrors(clientErrors)) return;
    setSaving(true);
    try {
      await onSubmit(mode === "create" ? createBody() : editBody());
    } catch (err) {
      setSaving(false);
      if (err instanceof OrderError && err.fields.length > 0) {
        const mapped: ErrandFormErrors = {};
        for (const field of err.fields) {
          // Item field paths arrive as items[0].description; show them on the row.
          const match = /^items\[(\d+)\]/.exec(field.field);
          if (match) {
            const index = Number(match[1]);
            const rows = mapped.itemErrors ?? draft.items.map(() => undefined);
            rows[index] = field.message;
            mapped.itemErrors = rows;
          } else if (field.field === "deliveryBuildingId" || field.field === "supplierId") {
            mapped[field.field] = field.message;
          } else {
            mapped[field.field as keyof ErrandFormErrors] = field.message as never;
          }
        }
        setServerFieldErrors(mapped);
        setFormError("Please correct the highlighted fields.");
      } else {
        setFormError(err instanceof OrderError ? err.message : "Could not save this errand.");
      }
    }
  }

  function requestCancel() {
    if (dirty) setConfirmCancel(true);
    else onCancel();
  }

  const show = (field: keyof ErrandFormErrors) =>
    (submitted || serverFieldErrors[field]) && errors[field]
      ? (errors[field] as string)
      : "";

  const itemMessages = (submitted ? errors.itemErrors : serverFieldErrors.itemErrors) ?? [];
  // Live buildings only; a retired one cannot be assigned, but an errand being
  // edited keeps the building it already has until it is changed.
  const buildingOptions = buildings.filter(
    (b) => !b.retired || b.id === original.deliveryBuildingId,
  );

  const head = (
    <div className="page-head">
      <div>
        <button type="button" className="link-btn" onClick={requestCancel}>
          ← Cancel
        </button>
        <h1 style={{ marginTop: "0.4rem" }}>
          {mode === "edit" ? "Edit errand" : template ? "Order again" : "Request an errand"}
        </h1>
        <p className="muted" style={{ margin: "0.3rem 0 0" }}>
          {mode === "edit"
            ? "Only an open errand can be edited."
            : `${template ? `Copied from your order of ${formatDateTime(template.createdAt)}. ` : ""}Your credits are reserved when you submit, and returned if nobody accepts in time.`}
        </p>
      </div>
    </div>
  );

  if (!ready) {
    return (
      <>
        {head}
        <p className="muted center mt">Loading…</p>
      </>
    );
  }

  return (
    <>
      {head}

      {formError && (
        <div className="banner banner-error" role="alert">
          <span>{formError}</span>
        </div>
      )}

      {dropped.supplier && (
        <div className="banner banner-wait" role="status">
          <span>The pickup location from your last order is no longer available. Choose another.</span>
        </div>
      )}

      {dropped.building && (
        <div className="banner banner-wait" role="status">
          <span>The drop-off building from your last order is no longer available. Choose another.</span>
        </div>
      )}

      {rewardChanged && (
        <div className="banner banner-wait" role="status">
          <span>
            Changing the reward needs the credit service to adjust your reservation, so this edit
            waits for it. Couriers keep seeing the current details and can accept them meanwhile.
          </span>
        </div>
      )}

      <form className="panel" onSubmit={onFormSubmit} noValidate>
        <h2>Pickup</h2>
        <Field label="Find a pickup location" error="">
          <input
            className="input"
            type="search"
            value={supplierQuery}
            placeholder="Search by name or location…"
            onChange={(e) => setSupplierQuery(e.target.value)}
          />
        </Field>
        {supplierError && <p className="hint error">{supplierError}</p>}
        <Field label="Supplier" error={show("supplierId")}>
          <select
            className="input select"
            value={draft.supplierId}
            onChange={(e) => set("supplierId", e.target.value)}
            aria-invalid={!!show("supplierId")}
          >
            <option value="">Choose a supplier…</option>
            {/* Keep the current choice selectable when it is outside the search. */}
            {needsFallbackOption && (
              <option value={draft.supplierId}>{selectedLabel}</option>
            )}
            {suppliers.map((s) => (
              <option key={s.supplierId} value={s.supplierId}>
                {s.name} — {s.building}
              </option>
            ))}
          </select>
        </Field>
        {chosen && chosen.supplierId === draft.supplierId ? (
          <p className="muted small">
            {chosen.building} · Floor {chosen.floor} · open {chosen.openingTime}–
            {chosen.closingTime}. An errand can only be created while the supplier is open.
          </p>
        ) : (
          selectedLabel && (
            <p className="muted small">
              {source?.pickup.building} · Floor {source?.pickup.floor}. Search above to change
              the pickup location.
            </p>
          )
        )}

        <h2 className="mt">Drop-off</h2>
        <div className="form-grid">
          <Field label="Building" error={show("deliveryBuildingId")}>
            <select
              className="input select"
              value={draft.deliveryBuildingId}
              onChange={(e) => set("deliveryBuildingId", e.target.value)}
              aria-invalid={!!show("deliveryBuildingId")}
            >
              <option value="">Choose a building…</option>
              {buildingOptions.map((b) => (
                <option key={b.id} value={b.id}>
                  {b.name}
                  {b.retired ? " (retired)" : ""}
                </option>
              ))}
            </select>
          </Field>
          <Field label="Reward (credits)" error={show("reward")}>
            <input
              className="input"
              value={draft.reward}
              inputMode="numeric"
              placeholder="5"
              onChange={(e) => set("reward", e.target.value)}
              aria-invalid={!!show("reward")}
            />
          </Field>
        </div>

        <Field label="Delivery detail" error={show("deliveryDetail")}>
          <input
            className="input"
            value={draft.deliveryDetail}
            maxLength={LIMITS.deliveryDetail}
            placeholder="e.g. Block B, level 7 lounge"
            onChange={(e) => set("deliveryDetail", e.target.value)}
            aria-invalid={!!show("deliveryDetail")}
          />
        </Field>

        <h2 className="mt">Items</h2>
        {show("items") && <p className="hint error">{show("items")}</p>}
        <ul className="item-rows">
          {draft.items.map((item, index) => (
            <li key={index}>
              <div className="item-row">
                <input
                  className="input"
                  value={item.description}
                  maxLength={LIMITS.itemDescription}
                  placeholder="What should the courier buy or collect?"
                  onChange={(e) => setItem(index, { description: e.target.value })}
                  aria-label={`Item ${index + 1} description`}
                  aria-invalid={!!itemMessages[index]}
                />
                <input
                  className="input qty-input"
                  value={item.quantity}
                  inputMode="numeric"
                  onChange={(e) => setItem(index, { quantity: e.target.value })}
                  aria-label={`Item ${index + 1} quantity`}
                  aria-invalid={!!itemMessages[index]}
                />
                <button
                  type="button"
                  className="btn outline slim"
                  onClick={() => removeItem(index)}
                  disabled={draft.items.length === 1}
                  aria-label={`Remove item ${index + 1}`}
                >
                  ✕
                </button>
              </div>
              {itemMessages[index] && <p className="hint error">{itemMessages[index]}</p>}
            </li>
          ))}
        </ul>
        <button
          type="button"
          className="btn outline slim"
          onClick={addItem}
          disabled={draft.items.length >= LIMITS.items}
        >
          + Add item
        </button>
        <p className="hint muted">
          {draft.items.length}/{LIMITS.items} items
        </p>

        <h2 className="mt">Courier instructions</h2>
        <Field label="Notes (optional)" error={show("notes")}>
          <textarea
            className="input"
            rows={3}
            value={draft.notes}
            maxLength={LIMITS.notes}
            placeholder="Anything the courier should know. Only the assigned courier sees this."
            onChange={(e) => set("notes", e.target.value)}
            aria-invalid={!!show("notes")}
          />
        </Field>

        <div className="form-grid">
          <Field label="Contact name" error={show("contactName")}>
            <input
              className="input"
              value={draft.contactName}
              maxLength={LIMITS.contactName}
              onChange={(e) => set("contactName", e.target.value)}
              aria-invalid={!!show("contactName")}
            />
          </Field>
          <Field label="Contact mobile" error={show("contactMobile")}>
            <input
              className="input"
              value={draft.contactMobile}
              maxLength={LIMITS.contactMobile}
              onChange={(e) => set("contactMobile", e.target.value)}
              aria-invalid={!!show("contactMobile")}
            />
          </Field>
        </div>
        <p className="hint muted">
          Prefilled from your profile. Change it if someone else should be contacted about this
          errand.
        </p>

        <Field label="Accept by" error={show("acceptanceDeadline")}>
          <input
            className="input"
            type="datetime-local"
            value={draft.acceptanceDeadline}
            onChange={(e) => set("acceptanceDeadline", e.target.value)}
            aria-invalid={!!show("acceptanceDeadline")}
          />
        </Field>
        <p className="hint muted">
          Between 10 minutes and 24 hours from now. If nobody accepts by then, the errand expires
          and your credits are returned.
        </p>

        <div className="actions">
          <button
            type="submit"
            className="btn slim"
            disabled={saving || (mode === "edit" && !dirty)}
          >
            {saving
              ? "Saving…"
              : mode === "create"
                ? `Request errand${draft.reward && /^\d+$/.test(draft.reward) ? ` · ${creditText(Number(draft.reward))}` : ""}`
                : "Save changes"}
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
              <button
                type="button"
                className="btn outline slim"
                onClick={() => setConfirmCancel(false)}
              >
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
