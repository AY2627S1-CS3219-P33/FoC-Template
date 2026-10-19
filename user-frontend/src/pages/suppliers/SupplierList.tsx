import type { Lookup, Supplier, SupplierSort } from "../../api/supplier";

interface Props {
  items: Supplier[];
  loading: boolean;
  error: string | null;
  // Controlled filter state; the service applies all of it.
  query: string;
  type: string;
  building: string;
  sort: SupplierSort;
  types: Lookup[];
  buildings: Lookup[];
  hasMore: boolean;
  isAdmin: boolean;
  onQueryChange: (value: string) => void;
  onTypeChange: (value: string) => void;
  onBuildingChange: (value: string) => void;
  onSortChange: (value: SupplierSort) => void;
  onOpen: (supplier: Supplier) => void;
  onNew: () => void;
  onLoadMore: () => void;
  onManageLookups?: () => void;
}

const SORTS: { value: SupplierSort; label: string }[] = [
  { value: "name", label: "Name A–Z" },
  { value: "-name", label: "Name Z–A" },
  { value: "type", label: "Type A–Z" },
  { value: "-type", label: "Type Z–A" },
  { value: "building", label: "Building A–Z" },
  { value: "-building", label: "Building Z–A" },
];

export function SupplierList(props: Props) {
  const { items, loading, error, query, type, building, sort, types, buildings, hasMore, isAdmin } =
    props;

  // Only live lookups can be assigned, but a filter may still name a retired
  // one, so keep the active value selectable rather than silently dropping it.
  function options(lookups: Lookup[], active: string) {
    const live = lookups.filter((item) => !item.retired);
    if (active !== "" && !live.some((item) => item.name === active)) {
      const retired = lookups.find((item) => item.name === active);
      if (retired) return [retired, ...live];
    }
    return live;
  }

  return (
    <>
      <div className="page-head">
        <div>
          <h1>Suppliers</h1>
          <p className="muted" style={{ margin: "0.3rem 0 0" }}>
            Pickup locations across campus.
          </p>
        </div>
        {isAdmin && (
          <div className="actions" style={{ marginTop: 0 }}>
            {props.onManageLookups && (
              <button type="button" className="btn outline slim" onClick={props.onManageLookups}>
                Buildings & types
              </button>
            )}
            <button type="button" className="btn slim" onClick={props.onNew}>
              + New supplier
            </button>
          </div>
        )}
      </div>

      <div className="toolbar">
        <input
          className="input search"
          type="search"
          placeholder="Search name or location…"
          value={query}
          onChange={(e) => props.onQueryChange(e.target.value)}
          aria-label="Search suppliers"
        />
        <select
          className="input select"
          value={type}
          onChange={(e) => props.onTypeChange(e.target.value)}
          aria-label="Filter by type"
        >
          <option value="">All types</option>
          {options(types, type).map((item) => (
            <option key={item.id} value={item.name}>
              {item.name}
              {item.retired ? " (retired)" : ""}
            </option>
          ))}
        </select>
        <select
          className="input select"
          value={building}
          onChange={(e) => props.onBuildingChange(e.target.value)}
          aria-label="Filter by building"
        >
          <option value="">All buildings</option>
          {options(buildings, building).map((item) => (
            <option key={item.id} value={item.name}>
              {item.name}
              {item.retired ? " (retired)" : ""}
            </option>
          ))}
        </select>
        <select
          className="input select"
          value={sort}
          onChange={(e) => props.onSortChange(e.target.value as SupplierSort)}
          aria-label="Sort suppliers"
        >
          {SORTS.map((option) => (
            <option key={option.value} value={option.value}>
              {option.label}
            </option>
          ))}
        </select>
      </div>

      {error && (
        <div className="banner banner-error" role="alert">
          <span>{error}</span>
        </div>
      )}

      {!error && !loading && items.length === 0 && (
        <div className="placeholder">
          <p>No suppliers match your search.</p>
          {building !== "" && (
            <p className="muted">
              A retired building has no current suppliers, so filtering by one is always empty.
            </p>
          )}
        </div>
      )}

      <div className="supplier-grid">
        {items.map((s) => (
          <button
            key={s.supplierId}
            type="button"
            className="supplier-card"
            onClick={() => props.onOpen(s)}
          >
            <div className="supplier-thumb" aria-hidden>
              {s.imageUrl ? <img src={s.imageUrl} alt="" loading="lazy" /> : <span>photo</span>}
            </div>
            <div className="supplier-body">
              <div className="supplier-row">
                <h3>{s.name}</h3>
                <span className="type-chip">{s.type}</span>
              </div>
              <p className="supplier-loc">
                {s.building} · Floor {s.floor}
              </p>
              <p className="supplier-sub">{s.locationDescription}</p>
              <p className="supplier-hours">
                {s.openingTime}–{s.closingTime}
              </p>
            </div>
          </button>
        ))}
      </div>

      {loading && <p className="muted center mt">Loading…</p>}

      {hasMore && !loading && (
        <div className="center mt">
          <button type="button" className="btn outline slim" onClick={props.onLoadMore}>
            Load more
          </button>
        </div>
      )}
    </>
  );
}
