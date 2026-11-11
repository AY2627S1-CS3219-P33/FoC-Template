// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: User-frontend suppliers page and SupplierDetail behavior.
// Author review: I validated correctness, edited for style, and added boundary checks.

import { useState } from "react";
import { SupplierError, type Supplier } from "../../api/supplier";

interface Props {
  supplier: Supplier;
  isAdmin: boolean;
  onBack: () => void;
  onEdit: () => void;
  onDelete: () => Promise<void>;
  onRequestErrand?: () => void;
}

export function SupplierDetail({ supplier, isAdmin, onBack, onEdit, onDelete, onRequestErrand }: Props) {
  const [confirming, setConfirming] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function doDelete() {
    setDeleting(true);
    setError(null);
    try {
      await onDelete();
    } catch (err) {
      setDeleting(false);
      setConfirming(false);
      if (err instanceof SupplierError && err.code === "SUPPLIER_HAS_ACTIVE_ERRANDS") {
        setError("This supplier is used by an active errand and cannot be deleted yet.");
      } else if (err instanceof SupplierError) {
        setError(err.message);
      } else {
        setError("Could not delete this supplier.");
      }
    }
  }

  return (
    <>
      <div className="page-head">
        <div>
          <button type="button" className="link-btn" onClick={onBack}>
            ← Back to suppliers
          </button>
          <h1 style={{ marginTop: "0.4rem" }}>{supplier.name}</h1>
          <div className="head-meta">
            <span className="type-chip">{supplier.type}</span>
          </div>
        </div>
        {onRequestErrand && (
          <button type="button" className="btn slim" onClick={onRequestErrand}>
            Request an errand
          </button>
        )}
        {isAdmin && (
          <div className="actions" style={{ marginTop: 0 }}>
            <button type="button" className="btn slim" onClick={onEdit}>
              Edit
            </button>
            <button
              type="button"
              className="btn danger slim"
              onClick={() => setConfirming(true)}
            >
              Delete
            </button>
          </div>
        )}
      </div>

      {error && (
        <div className="banner banner-error" role="alert">
          <span>{error}</span>
        </div>
      )}

      {supplier.imageUrl && (
        <div className="detail-image">
          <img src={supplier.imageUrl} alt={supplier.name} loading="lazy" />
        </div>
      )}

      <section className="panel">
        <h2>Location</h2>
        <dl className="fields">
          <div>
            <dt>Building</dt>
            <dd>{supplier.building}</dd>
          </div>
          <div>
            <dt>Floor</dt>
            <dd>{supplier.floor}</dd>
          </div>
          <div className="full">
            <dt>Description</dt>
            <dd>{supplier.locationDescription}</dd>
          </div>
          <div>
            <dt>Latitude</dt>
            <dd className="mono">{supplier.latitude}</dd>
          </div>
          <div>
            <dt>Longitude</dt>
            <dd className="mono">{supplier.longitude}</dd>
          </div>
        </dl>
      </section>

      <section className="panel">
        <h2>Hours</h2>
        <dl className="fields">
          <div>
            <dt>Opening</dt>
            <dd>{supplier.openingTime}</dd>
          </div>
          <div>
            <dt>Closing</dt>
            <dd>{supplier.closingTime}</dd>
          </div>
        </dl>
      </section>

      {confirming && (
        <div className="modal-backdrop" role="dialog" aria-modal="true" aria-label="Confirm delete">
          <div className="modal">
            <h2>Delete supplier?</h2>
            <p className="muted">
              This removes <b>{supplier.name}</b> from the current catalogue. Past
              errands keep their historical record. This cannot be undone.
            </p>
            <div className="actions">
              <button type="button" className="btn danger slim" onClick={doDelete} disabled={deleting}>
                {deleting ? "Deleting…" : "Delete"}
              </button>
              <button
                type="button"
                className="btn outline slim"
                onClick={() => setConfirming(false)}
                disabled={deleting}
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
