import { useCallback, useEffect, useRef, useState } from "react";
import { useAuth } from "../../auth/AuthContext";
import {
  SupplierError,
  createSupplier,
  deleteSupplier,
  listBuildings,
  listSuppliers,
  listSupplierTypes,
  updateSupplier,
  type Lookup,
  type Supplier,
  type SupplierSort,
  type SupplierWrite,
  type SupplierPatch,
} from "../../api/supplier";
import type { Profile } from "../../api/types";
import { SupplierList } from "./SupplierList";
import { SupplierDetail } from "./SupplierDetail";
import { SupplierForm } from "./SupplierForm";
import { LookupsPage } from "../admin/LookupsPage";

type View =
  | { kind: "list" }
  | { kind: "detail"; supplier: Supplier }
  | { kind: "create" }
  | { kind: "edit"; supplier: Supplier }
  | { kind: "lookups" };

const PAGE_SIZE = 12;
const SUPPLIER_AUDIENCE =
  import.meta.env.VITE_SUPPLIER_AUDIENCE || "https://api.foc.local/supplier-service";

export function SuppliersPage() {
  const { profile, getAccessToken } = useAuth();
  const account = profile as Profile;
  const isAdmin = account.role === "ADMIN" || account.role === "SUPER_ADMIN";

  const getSupplierToken = useCallback(
    (manage = false) =>
      getAccessToken({
        audience: SUPPLIER_AUDIENCE,
        scope: manage
          ? "openid profile email suppliers:read suppliers:manage"
          : "openid profile email suppliers:read",
      }),
    [getAccessToken],
  );

  const [view, setView] = useState<View>({ kind: "list" });

  // List state. q, type, building and sort are all applied by the service, so
  // changing any of them starts a new page sequence: a cursor is only valid
  // with the filters and sort that produced it.
  const [items, setItems] = useState<Supplier[]>([]);
  const [nextCursor, setNextCursor] = useState<string | undefined>();
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [query, setQuery] = useState("");
  const [type, setType] = useState("");
  const [building, setBuilding] = useState("");
  const [sort, setSort] = useState<SupplierSort>("name");
  const requestId = useRef(0);

  // Lookups fill the filters and the form's dropdowns. Retired rows are
  // included so an existing supplier's building or type still has a label.
  const [types, setTypes] = useState<Lookup[]>([]);
  const [buildings, setBuildings] = useState<Lookup[]>([]);
  const [lookupError, setLookupError] = useState<string | null>(null);

  const loadLookups = useCallback(async () => {
    try {
      const token = await getSupplierToken();
      const [typeList, buildingList] = await Promise.all([
        listSupplierTypes(token, true),
        listBuildings(token, true),
      ]);
      setTypes(typeList.items);
      setBuildings(buildingList.items);
      setLookupError(null);
    } catch {
      setLookupError("Could not load the building and type lists.");
    }
  }, [getSupplierToken]);

  useEffect(() => {
    void loadLookups();
  }, [loadLookups]);

  const load = useCallback(
    async (
      q: string,
      t: string,
      b: string,
      s: SupplierSort,
      cursor?: string,
    ) => {
      const id = ++requestId.current;
      setLoading(true);
      setError(null);
      try {
        const token = await getSupplierToken();
        const page = await listSuppliers(token, {
          q: q || undefined,
          type: t || undefined,
          building: b || undefined,
          sort: s,
          limit: PAGE_SIZE,
          cursor,
        });
        if (id !== requestId.current) return; // a newer request superseded this
        setItems((prev) => (cursor ? [...prev, ...page.items] : page.items));
        setNextCursor(page.nextCursor);
      } catch (err) {
        if (id !== requestId.current) return;
        setError(messageFor(err));
      } finally {
        if (id === requestId.current) setLoading(false);
      }
    },
    [getSupplierToken],
  );

  // Debounced reload when the search, either filter or the sort changes (and on
  // first mount).
  useEffect(() => {
    const handle = setTimeout(() => load(query, type, building, sort), 250);
    return () => clearTimeout(handle);
  }, [query, type, building, sort, load]);

  const reload = useCallback(
    () => load(query, type, building, sort),
    [load, query, type, building, sort],
  );

  async function onCreate(body: SupplierWrite): Promise<void> {
    const token = await getSupplierToken(true);
    await createSupplier(token, body);
    setView({ kind: "list" });
    await reload();
  }

  async function onUpdate(id: string, patch: SupplierPatch): Promise<void> {
    const token = await getSupplierToken(true);
    const updated = await updateSupplier(token, id, patch);
    setView({ kind: "detail", supplier: updated });
    await reload();
  }

  async function onDelete(supplier: Supplier): Promise<void> {
    const token = await getSupplierToken(true);
    await deleteSupplier(token, supplier.supplierId);
    setView({ kind: "list" });
    await reload();
  }

  if (view.kind === "lookups") {
    return (
      <>
        <button type="button" className="link-btn" onClick={() => {
          setView({ kind: "list" });
          void loadLookups();
          void reload();
        }}>
          ← Back to suppliers
        </button>
        <LookupsPage getToken={getSupplierToken} />
      </>
    );
  }

  if (view.kind === "create") {
    return (
      <SupplierForm
        mode="create"
        types={types}
        buildings={buildings}
        onSubmit={(body) => onCreate(body)}
        onCancel={() => setView({ kind: "list" })}
      />
    );
  }
  if (view.kind === "edit") {
    return (
      <SupplierForm
        mode="edit"
        initial={view.supplier}
        types={types}
        buildings={buildings}
        onSubmit={(body) => onUpdate(view.supplier.supplierId, body)}
        onCancel={() => setView({ kind: "detail", supplier: view.supplier })}
      />
    );
  }
  if (view.kind === "detail") {
    return (
      <SupplierDetail
        supplier={view.supplier}
        isAdmin={isAdmin}
        onBack={() => setView({ kind: "list" })}
        onEdit={() => setView({ kind: "edit", supplier: view.supplier })}
        onDelete={() => onDelete(view.supplier)}
      />
    );
  }

  return (
    <>
      {lookupError && (
        <div className="banner banner-error" role="alert">
          <span>{lookupError}</span>
        </div>
      )}
      <SupplierList
        items={items}
        loading={loading}
        error={error}
        query={query}
        type={type}
        building={building}
        sort={sort}
        types={types}
        buildings={buildings}
        hasMore={Boolean(nextCursor)}
        isAdmin={isAdmin}
        onQueryChange={setQuery}
        onTypeChange={setType}
        onBuildingChange={setBuilding}
        onSortChange={setSort}
        onOpen={(supplier) => setView({ kind: "detail", supplier })}
        onNew={() => setView({ kind: "create" })}
        onLoadMore={() => load(query, type, building, sort, nextCursor)}
        onManageLookups={isAdmin ? () => setView({ kind: "lookups" }) : undefined}
      />
    </>
  );
}

function messageFor(err: unknown): string {
  if (err instanceof SupplierError) {
    if (err.code === "UNAUTHENTICATED") return "Please sign in again to view suppliers.";
    if (err.code === "DEPENDENCY_UNAVAILABLE")
      return "The supplier service is temporarily unavailable.";
    return err.message;
  }
  // A network/parse failure usually means the supplier backend is not running.
  return "Could not reach the supplier service.";
}
