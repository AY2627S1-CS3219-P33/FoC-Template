import { useCallback, useEffect, useRef, useState } from "react";
import { useAuth } from "../../auth/AuthContext";
import {
  SupplierError,
  createSupplier,
  deleteSupplier,
  listSuppliers,
  updateSupplier,
  type Supplier,
  type SupplierWrite,
  type SupplierPatch,
} from "../../api/supplier";
import type { Profile } from "../../api/types";
import { SupplierList } from "./SupplierList";
import { SupplierDetail } from "./SupplierDetail";
import { SupplierForm } from "./SupplierForm";

type View =
  | { kind: "list" }
  | { kind: "detail"; supplier: Supplier }
  | { kind: "create" }
  | { kind: "edit"; supplier: Supplier };

const PAGE_SIZE = 12;
const SUPPLIER_AUDIENCE =
  import.meta.env.VITE_SUPPLIER_AUDIENCE || "https://api.foc.local/supplier-service";

export function SuppliersPage() {
  const { profile, getAccessToken } = useAuth();
  const account = profile as Profile;
  const isAdmin = account.role === "ADMIN" || account.role === "SUPER_ADMIN";

  const getSupplierToken = useCallback(
    (manage = false) => getAccessToken({
      audience: SUPPLIER_AUDIENCE,
      scope: manage
        ? "openid profile email suppliers:read suppliers:manage"
        : "openid profile email suppliers:read",
    }),
    [getAccessToken],
  );

  const [view, setView] = useState<View>({ kind: "list" });

  // List state
  const [items, setItems] = useState<Supplier[]>([]);
  const [nextCursor, setNextCursor] = useState<string | undefined>();
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [query, setQuery] = useState("");
  const [type, setType] = useState("");
  const requestId = useRef(0);

  const load = useCallback(
    async (q: string, t: string, cursor?: string) => {
      const id = ++requestId.current;
      setLoading(true);
      setError(null);
      try {
        const token = await getSupplierToken();
        const page = await listSuppliers(token, {
          q: q || undefined,
          type: t || undefined,
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

  // Debounced reload when search or filter changes (and on first mount).
  useEffect(() => {
    const handle = setTimeout(() => load(query, type), 250);
    return () => clearTimeout(handle);
  }, [query, type, load]);

  async function onCreate(body: SupplierWrite): Promise<void> {
    const token = await getSupplierToken(true);
    await createSupplier(token, body);
    setView({ kind: "list" });
    await load(query, type);
  }

  async function onUpdate(id: string, patch: SupplierPatch): Promise<void> {
    const token = await getSupplierToken(true);
    const updated = await updateSupplier(token, id, patch);
    setView({ kind: "detail", supplier: updated });
    await load(query, type);
  }

  async function onDelete(supplier: Supplier): Promise<void> {
    const token = await getSupplierToken(true);
    await deleteSupplier(token, supplier.supplierId);
    setView({ kind: "list" });
    await load(query, type);
  }

  if (view.kind === "create") {
    return (
      <SupplierForm
        mode="create"
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
      <SupplierList
        items={items}
        loading={loading}
        error={error}
        query={query}
        type={type}
        hasMore={Boolean(nextCursor)}
        isAdmin={isAdmin}
        onQueryChange={setQuery}
        onTypeChange={setType}
        onOpen={(supplier) => setView({ kind: "detail", supplier })}
        onNew={() => setView({ kind: "create" })}
        onLoadMore={() => load(query, type, nextCursor)}
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
