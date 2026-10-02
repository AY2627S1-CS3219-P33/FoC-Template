import { useMemo, useState } from "react";
import type { Supplier } from "../../api/supplier";

type SortKey = "name-asc" | "name-desc" | "type-asc";

interface Props {
  items: Supplier[];
  loading: boolean;
  error: string | null;
  query: string;
  type: string;
  hasMore: boolean;
  isAdmin: boolean;
  onQueryChange: (value: string) => void;
  onTypeChange: (value: string) => void;
  onOpen: (supplier: Supplier) => void;
  onNew: () => void;
  onLoadMore: () => void;
}

export function SupplierList(props: Props) {
  const { items, loading, error, query, type, hasMore, isAdmin } = props;
  const [sort, setSort] = useState<SortKey>("name-asc");

  // Type options derive from what is currently loaded, so the filter always
  // reflects real data rather than a hard-coded list.
  const types = useMemo(() => {
    const set = new Set(items.map((s) => s.type));
    return Array.from(set).sort((a, b) => a.localeCompare(b));
  }, [items]);

  // Server returns name-ascending; sort is applied to the loaded rows for
  // immediate control without refetching.
  const sorted = useMemo(() => {
    const rows = [...items];
    rows.sort((a, b) => {
      if (sort === "type-asc") {
        const byType = a.type.localeCompare(b.type);
        return byType !== 0 ? byType : a.name.localeCompare(b.name);
      }
      const byName = a.name.localeCompare(b.name);
      return sort === "name-desc" ? -byName : byName;
    });
    return rows;
  }, [items, sort]);

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
          <button type="button" className="btn slim" onClick={props.onNew}>
            + New supplier
          </button>
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
          {/* Include the active filter even if absent from the loaded page. */}
          {type && !types.includes(type) && <option value={type}>{type}</option>}
          {types.map((t) => (
            <option key={t} value={t}>
              {t}
            </option>
          ))}
        </select>
        <select
          className="input select"
          value={sort}
          onChange={(e) => setSort(e.target.value as SortKey)}
          aria-label="Sort suppliers"
        >
          <option value="name-asc">Name A–Z</option>
          <option value="name-desc">Name Z–A</option>
          <option value="type-asc">Type</option>
        </select>
      </div>

      {error && (
        <div className="banner banner-error" role="alert">
          <span>{error}</span>
        </div>
      )}

      {!error && !loading && sorted.length === 0 && (
        <div className="placeholder">
          <p>No suppliers match your search.</p>
        </div>
      )}

      <div className="supplier-grid">
        {sorted.map((s) => (
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
