// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: User-frontend SavedPlaces page and SavedPlaces behavior.
// Author review: I validated correctness, edited for style, and added boundary checks.

import { useCallback, useEffect, useState, type FormEvent } from "react";
import { useAuth } from "../auth/AuthContext";
import { deletePlace, listPlaces, updatePlace, type PlacePatch, type SavedPlace } from "../api/places";
import { placeError } from "./errands/messages";
import { LIMITS } from "../orderValidation";

// Saved places belong to order-service; the Profile page shows them.
const ORDER_AUDIENCE =
  import.meta.env.VITE_ORDER_AUDIENCE || "https://api.foc.local/order-service";

// Saved place labels are at most 40 characters (order-service PlaceLabel).
const PLACE_LABEL_MAX = 40;

// The caller's saved drop-offs and pickup locations. They are added from the
// errand form; here they can be renamed or removed. A stale place (retired
// building, deleted supplier) is kept until the user removes it.
export function SavedPlaces() {
  const { getAccessToken } = useAuth();
  const getOrderToken = useCallback(
    () =>
      getAccessToken({
        audience: ORDER_AUDIENCE,
        scope: "openid profile email errands:use",
      }),
    [getAccessToken],
  );

  const [places, setPlaces] = useState<SavedPlace[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [editing, setEditing] = useState<SavedPlace | null>(null);
  const [removing, setRemoving] = useState<SavedPlace | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const token = await getOrderToken();
      const list = await listPlaces(token);
      setPlaces(list.items);
      setError(null);
    } catch (err) {
      setError(placeError(err));
    } finally {
      setLoading(false);
    }
  }, [getOrderToken]);

  useEffect(() => {
    load();
  }, [load]);

  async function onRemove() {
    if (!removing) return;
    setBusy(true);
    setError(null);
    setNotice(null);
    try {
      const token = await getOrderToken();
      await deletePlace(token, removing.placeId);
      setNotice(`Removed "${removing.label}".`);
    } catch (err) {
      setError(placeError(err));
    } finally {
      setBusy(false);
      setRemoving(null);
      await load();
    }
  }

  const delivery = places.filter((p) => p.kind === "DELIVERY");
  const pickup = places.filter((p) => p.kind === "PICKUP");

  return (
    <section className="panel">
      <h2>Saved places</h2>
      <p className="muted" style={{ marginTop: 0 }}>
        Save drop-offs and pickup locations when you request an errand, then pick them in one
        click next time.
      </p>

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

      {loading ? (
        <p className="muted">Loading…</p>
      ) : (
        <>
          <PlaceGroup
            title="Drop-offs"
            empty="No saved drop-offs yet."
            places={delivery}
            onEdit={setEditing}
            onRemove={setRemoving}
          />
          <PlaceGroup
            title="Pickup locations"
            empty="No saved pickup locations yet."
            places={pickup}
            onEdit={setEditing}
            onRemove={setRemoving}
          />
        </>
      )}

      {editing && (
        <RenameDialog
          place={editing}
          getToken={getOrderToken}
          onDone={async (label) => {
            setEditing(null);
            setError(null);
            setNotice(label ? `Saved "${label}".` : null);
            await load();
          }}
        />
      )}

      {removing && (
        <div className="modal-backdrop" role="dialog" aria-modal="true" aria-label="Remove place">
          <div className="modal">
            <h2>Remove "{removing.label}"?</h2>
            <p className="muted">
              Errands you already requested are not affected. You can save it again later.
            </p>
            <div className="actions">
              <button type="button" className="btn danger slim" onClick={onRemove} disabled={busy}>
                {busy ? "Removing…" : "Remove"}
              </button>
              <button
                type="button"
                className="btn outline slim"
                onClick={() => setRemoving(null)}
                disabled={busy}
              >
                Keep
              </button>
            </div>
          </div>
        </div>
      )}
    </section>
  );
}

function PlaceGroup({
  title,
  empty,
  places,
  onEdit,
  onRemove,
}: {
  title: string;
  empty: string;
  places: SavedPlace[];
  onEdit: (place: SavedPlace) => void;
  onRemove: (place: SavedPlace) => void;
}) {
  return (
    <>
      <h3 className="place-group-title">{title}</h3>
      {places.length === 0 ? (
        <p className="muted small">{empty}</p>
      ) : (
        <ul className="place-list">
          {places.map((place) => (
            <li key={place.placeId} className="row-between">
              <div>
                <b>{place.label}</b>{" "}
                {!place.available && (
                  <span className="status-chip tone-bad">No longer available</span>
                )}
                <p className="muted small" style={{ margin: "0.2rem 0 0" }}>
                  {describe(place)}
                </p>
                {!place.available && (
                  <p className="hint muted" style={{ margin: "0.2rem 0 0" }}>
                    It can no longer be used for new errands. Remove it.
                  </p>
                )}
              </div>
              <div className="actions" style={{ margin: 0 }}>
                <button type="button" className="btn outline slim" onClick={() => onEdit(place)}>
                  Rename
                </button>
                <button type="button" className="btn outline slim" onClick={() => onRemove(place)}>
                  Remove
                </button>
              </div>
            </li>
          ))}
        </ul>
      )}
    </>
  );
}

function describe(place: SavedPlace): string {
  if (place.kind === "DELIVERY") {
    const building = place.deliveryBuilding.name || "Unknown building";
    return `${building}${place.deliveryBuilding.retired ? " (retired)" : ""} · ${place.deliveryDetail}`;
  }
  if (!place.supplier) return "This pickup location was removed.";
  const s = place.supplier;
  return `${s.name} · ${s.building}, floor ${s.floor} · open ${s.openingTime}–${s.closingTime}`;
}

// Renames a place; a drop-off's detail can change too. The kind, building and
// supplier cannot: remove the place and save another.
function RenameDialog({
  place,
  getToken,
  onDone,
}: {
  place: SavedPlace;
  getToken: () => Promise<string>;
  onDone: (savedLabel: string | null) => void;
}) {
  const [label, setLabel] = useState(place.label);
  const [detail, setDetail] = useState(place.kind === "DELIVERY" ? place.deliveryDetail : "");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function onSave(event: FormEvent) {
    event.preventDefault();
    const patch: PlacePatch = {};
    if (label.trim() !== place.label) patch.label = label.trim();
    if (place.kind === "DELIVERY" && detail.trim() !== place.deliveryDetail)
      patch.deliveryDetail = detail.trim();
    if (label.trim() === "") return setError("Give this place a name.");
    if (place.kind === "DELIVERY" && detail.trim() === "")
      return setError("Enter the delivery detail.");
    if (Object.keys(patch).length === 0) return onDone(null);
    setBusy(true);
    setError(null);
    try {
      const token = await getToken();
      const updated = await updatePlace(token, place.placeId, patch);
      onDone(updated.label);
    } catch (err) {
      setBusy(false);
      setError(placeError(err));
    }
  }

  return (
    <div className="modal-backdrop" role="dialog" aria-modal="true" aria-label="Rename place">
      <form className="modal" onSubmit={onSave} noValidate>
        <h2>Rename place</h2>
        {error && (
          <div className="banner banner-error" role="alert">
            <span>{error}</span>
          </div>
        )}
        <div className="field">
          <label htmlFor="place-label">Name</label>
          <input
            id="place-label"
            className="input"
            value={label}
            maxLength={PLACE_LABEL_MAX}
            onChange={(e) => setLabel(e.target.value)}
          />
        </div>
        {place.kind === "DELIVERY" && (
          <div className="field">
            <label htmlFor="place-detail">Delivery detail</label>
            <input
              id="place-detail"
              className="input"
              value={detail}
              maxLength={LIMITS.deliveryDetail}
              onChange={(e) => setDetail(e.target.value)}
            />
          </div>
        )}
        <div className="actions">
          <button type="submit" className="btn slim" disabled={busy}>
            {busy ? "Saving…" : "Save"}
          </button>
          <button
            type="button"
            className="btn outline slim"
            onClick={() => onDone(null)}
            disabled={busy}
          >
            Cancel
          </button>
        </div>
      </form>
    </div>
  );
}
