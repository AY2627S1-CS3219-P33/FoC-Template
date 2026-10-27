import { useCallback, useEffect, useRef, useState } from "react";
import { useAuth } from "../../auth/AuthContext";
import {
  createErrand,
  editErrand,
  type ErrandCreate,
  type ErrandEdit,
  type RequesterErrandView,
} from "../../api/order";
import { listBuildings, listSuppliers, type Lookup } from "../../api/supplier";
import type { Profile } from "../../api/types";
import { OpenErrandList } from "./OpenErrandList";
import { MyErrandList, type MyListKind } from "./MyErrandList";
import { ErrandDetail } from "./ErrandDetail";
import { ErrandForm } from "./ErrandForm";

type Tab = "browse" | MyListKind;

type View =
  | { kind: "list" }
  | { kind: "detail"; errandId: string }
  | { kind: "create" }
  | { kind: "edit"; errand: RequesterErrandView }
  // "Order again": a new create prefilled from a finished errand.
  | { kind: "reorder"; source: RequesterErrandView };

const ORDER_AUDIENCE =
  import.meta.env.VITE_ORDER_AUDIENCE || "https://api.foc.local/order-service";
const SUPPLIER_AUDIENCE =
  import.meta.env.VITE_SUPPLIER_AUDIENCE || "https://api.foc.local/supplier-service";

const TABS: { key: Tab; label: string }[] = [
  { key: "browse", label: "Browse" },
  { key: "requested", label: "Requested" },
  { key: "delivering", label: "Delivering" },
  { key: "history", label: "History" },
];

export function ErrandsPage() {
  const { profile, getAccessToken } = useAuth();
  const account = profile as Profile;

  const getOrderToken = useCallback(
    () =>
      getAccessToken({
        audience: ORDER_AUDIENCE,
        scope: "openid profile email errands:use",
      }),
    [getAccessToken],
  );

  // Pickup locations and delivery buildings belong to supplier-service, so the
  // picker and the filter need a token for that API.
  const getSupplierToken = useCallback(
    () =>
      getAccessToken({
        audience: SUPPLIER_AUDIENCE,
        scope: "openid profile email suppliers:read",
      }),
    [getAccessToken],
  );

  const [tab, setTab] = useState<Tab>("browse");
  const [view, setView] = useState<View>({ kind: "list" });
  // Bumped after any transition so the list behind the detail is refetched.
  const [refreshKey, setRefreshKey] = useState(0);

  const [buildings, setBuildings] = useState<Lookup[]>([]);
  const [suppliers, setSuppliers] = useState<{ supplierId: string; name: string }[]>([]);
  const [lookupError, setLookupError] = useState<string | null>(null);

  // One logical submission keeps one Idempotency-Key, so retrying an
  // interrupted request returns the first errand instead of creating a second.
  const idempotencyKey = useRef<string>(crypto.randomUUID());

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const token = await getSupplierToken();
        // Retired buildings are included so an errand that already references
        // one still shows its name; the form offers only live rows plus that one.
        const [buildingList, supplierPage] = await Promise.all([
          listBuildings(token, true),
          listSuppliers(token, { sort: "name", limit: 100 }),
        ]);
        if (cancelled) return;
        setBuildings(buildingList.items);
        setSuppliers(
          supplierPage.items.map((s) => ({ supplierId: s.supplierId, name: s.name })),
        );
      } catch {
        if (!cancelled)
          setLookupError("Could not load pickup locations and buildings from the supplier service.");
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [getSupplierToken]);

  const onChanged = useCallback(() => setRefreshKey((n) => n + 1), []);

  async function onCreate(body: ErrandCreate): Promise<void> {
    const token = await getOrderToken();
    const created = await createErrand(token, body, idempotencyKey.current);
    // The submission succeeded, so the next one is a new logical request.
    idempotencyKey.current = crypto.randomUUID();
    onChanged();
    setTab("requested");
    setView({ kind: "detail", errandId: created.errandId });
  }

  async function onEdit(errandId: string, body: ErrandEdit): Promise<void> {
    const token = await getOrderToken();
    await editErrand(token, errandId, body);
    onChanged();
    setView({ kind: "detail", errandId });
  }

  if (view.kind === "create") {
    return (
      <ErrandForm
        mode="create"
        profile={account}
        buildings={buildings}
        getSupplierToken={getSupplierToken}
        onSubmit={(body) => onCreate(body as ErrandCreate)}
        onCancel={() => setView({ kind: "list" })}
      />
    );
  }

  // Goes through onCreate like any new errand, so it gets that submission's
  // Idempotency-Key, never the old errand's.
  if (view.kind === "reorder") {
    return (
      <ErrandForm
        mode="create"
        template={view.source}
        profile={account}
        buildings={buildings}
        getSupplierToken={getSupplierToken}
        onSubmit={(body) => onCreate(body as ErrandCreate)}
        onCancel={() => setView({ kind: "list" })}
      />
    );
  }

  if (view.kind === "edit") {
    return (
      <ErrandForm
        mode="edit"
        initial={view.errand}
        profile={account}
        buildings={buildings}
        getSupplierToken={getSupplierToken}
        onSubmit={(body) => onEdit(view.errand.errandId, body as ErrandEdit)}
        onCancel={() => setView({ kind: "detail", errandId: view.errand.errandId })}
      />
    );
  }

  if (view.kind === "detail") {
    return (
      <ErrandDetail
        errandId={view.errandId}
        getToken={getOrderToken}
        onBack={() => setView({ kind: "list" })}
        onEdit={(errand) => setView({ kind: "edit", errand })}
        onOrderAgain={(source) => setView({ kind: "reorder", source })}
        onChanged={onChanged}
      />
    );
  }

  return (
    <>
      <div className="page-head">
        <div>
          <h1>Errands</h1>
          <p className="muted" style={{ margin: "0.3rem 0 0" }}>
            Request a pickup, or earn credits delivering for someone else.
          </p>
        </div>
        <button type="button" className="btn slim" onClick={() => setView({ kind: "create" })}>
          + Request errand
        </button>
      </div>

      {lookupError && (
        <div className="banner banner-error" role="alert">
          <span>{lookupError}</span>
        </div>
      )}

      <div className="tabs tabs-4" role="tablist">
        {TABS.map((item) => (
          <button
            key={item.key}
            type="button"
            role="tab"
            className="tab"
            aria-selected={tab === item.key}
            onClick={() => setTab(item.key)}
          >
            {item.label}
          </button>
        ))}
      </div>

      <div className="tab-body">
        {tab === "browse" ? (
          <OpenErrandList
            key={`browse-${refreshKey}`}
            getToken={getOrderToken}
            suppliers={suppliers}
            onOpen={(errandId) => setView({ kind: "detail", errandId })}
          />
        ) : (
          <MyErrandList
            key={`${tab}-${refreshKey}`}
            kind={tab}
            getToken={getOrderToken}
            onOpen={(errandId) => setView({ kind: "detail", errandId })}
            onCreate={() => setView({ kind: "create" })}
            onOrderAgain={(source) => setView({ kind: "reorder", source })}
          />
        )}
      </div>
    </>
  );
}
