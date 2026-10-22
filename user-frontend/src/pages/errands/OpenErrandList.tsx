import { useCallback, useState } from "react";
import { listOpenErrands, type OpenErrandItem } from "../../api/order";
import { ErrandCard } from "./ErrandCard";
import { usePagedList } from "./usePagedList";
import { errandError } from "./messages";

interface Props {
  getToken: () => Promise<string>;
  // Supplier options for the pickup filter, loaded once by ErrandsPage.
  suppliers: { supplierId: string; name: string }[];
  onOpen: (errandId: string) => void;
}

const PAGE_SIZE = 12;

type Coords = { lat: number; lng: number } | null;

export function OpenErrandList({ getToken, suppliers, onOpen }: Props) {
  const [supplierId, setSupplierId] = useState("");
  const [coords, setCoords] = useState<Coords>(null);
  const [locating, setLocating] = useState(false);
  const [locationError, setLocationError] = useState<string | null>(null);

  // Coordinates only reorder the page (nearest first) and are never stored by
  // the service; without them the order is time remaining.
  const load = useCallback(
    async (cursor?: string) => {
      const token = await getToken();
      return listOpenErrands(token, {
        supplierId: supplierId || undefined,
        lat: coords?.lat,
        lng: coords?.lng,
        limit: PAGE_SIZE,
        cursor,
      });
    },
    [getToken, supplierId, coords],
  );

  const list = usePagedList<OpenErrandItem>(load, errandError);

  function useMyLocation() {
    if (!navigator.geolocation) {
      setLocationError("This browser cannot share your location.");
      return;
    }
    setLocating(true);
    setLocationError(null);
    navigator.geolocation.getCurrentPosition(
      (position) => {
        setLocating(false);
        setCoords({
          lat: position.coords.latitude,
          lng: position.coords.longitude,
        });
      },
      () => {
        setLocating(false);
        setLocationError("Could not get your location. Showing the errands closing soonest.");
      },
      { timeout: 8000 },
    );
  }

  return (
    <>
      <div className="toolbar">
        <select
          className="input select"
          value={supplierId}
          onChange={(e) => setSupplierId(e.target.value)}
          aria-label="Filter by pickup location"
        >
          <option value="">All pickup locations</option>
          {suppliers.map((s) => (
            <option key={s.supplierId} value={s.supplierId}>
              {s.name}
            </option>
          ))}
        </select>

        {coords ? (
          <button
            type="button"
            className="btn outline slim"
            onClick={() => setCoords(null)}
            title="Order by time remaining instead"
          >
            ✓ Nearest first
          </button>
        ) : (
          <button type="button" className="btn outline slim" onClick={useMyLocation} disabled={locating}>
            {locating ? "Locating…" : "Nearest first"}
          </button>
        )}

        <button type="button" className="btn outline slim" onClick={list.reload} disabled={list.loading}>
          Refresh
        </button>
      </div>

      {locationError && (
        <div className="banner banner-error" role="alert">
          <span>{locationError}</span>
        </div>
      )}

      {list.error && (
        <div className="banner banner-error" role="alert">
          <span>{list.error}</span>
        </div>
      )}

      {!list.error && !list.loading && list.items.length === 0 && (
        <div className="placeholder">
          <p>No errands are waiting for a courier right now.</p>
          <p className="muted">
            Errands past their acceptance deadline are hidden, so check back shortly.
          </p>
        </div>
      )}

      <div className="errand-grid">
        {list.items.map((errand) => (
          <ErrandCard
            key={errand.errandId}
            pickup={errand.pickup}
            deliveryBuilding={errand.deliveryBuilding}
            deliveryDetail={errand.deliveryDetail}
            items={errand.items}
            reward={errand.reward}
            acceptanceDeadline={errand.acceptanceDeadline}
            distanceMeters={errand.distanceMeters}
            onOpen={() => onOpen(errand.errandId)}
          />
        ))}
      </div>

      {list.loading && <p className="muted center mt">Loading…</p>}

      {list.hasMore && !list.loading && (
        <div className="center mt">
          <button type="button" className="btn outline slim" onClick={list.loadMore}>
            Load more
          </button>
        </div>
      )}
    </>
  );
}
