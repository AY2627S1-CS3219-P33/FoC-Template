import { useCallback, useState } from "react";
import { useAuth } from "../../auth/AuthContext";
import { ReviewQueue } from "./ReviewQueue";
import { OrderSettingsForm } from "./OrderSettingsForm";
import { ErrandEvents } from "./ErrandEvents";
import { ActivityFlags } from "./ActivityFlags";
import { FailedOutbox } from "./FailedOutbox";

type Tab = "reviews" | "settings" | "events" | "flags" | "outbox";

const ORDER_AUDIENCE =
  import.meta.env.VITE_ORDER_AUDIENCE || "https://api.foc.local/order-service";

const TABS: { key: Tab; label: string; blurb: string }[] = [
  {
    key: "reviews",
    label: "Reviews",
    blurb: "Errands waiting on a decision: pay the courier, or refund the requester.",
  },
  {
    key: "settings",
    label: "Settings",
    blurb: "Deadlines and the thresholds that restrict couriers and flag requesters.",
  },
  {
    key: "events",
    label: "Events",
    blurb: "The transition log for every errand, newest first.",
  },
  {
    key: "flags",
    label: "Flags",
    blurb: "Users whose withdrawals or cancellations crossed the configured thresholds.",
  },
  {
    key: "outbox",
    label: "Outbox",
    blurb: "Credit commands and events that failed and need replaying.",
  },
];

export function OrderAdminPage() {
  const { getAccessToken } = useAuth();
  const [tab, setTab] = useState<Tab>("reviews");

  // Every route on this page requires errands:review, which is a different
  // permission from the errands:use the user screens ask for.
  const getToken = useCallback(
    () =>
      getAccessToken({
        audience: ORDER_AUDIENCE,
        scope: "openid profile email errands:review",
      }),
    [getAccessToken],
  );

  const active = TABS.find((item) => item.key === tab);

  return (
    <>
      <div className="page-head">
        <div>
          <h1>Errand administration</h1>
          <p className="muted" style={{ margin: "0.3rem 0 0" }}>
            {active?.blurb}
          </p>
        </div>
      </div>

      <div className="tabs tabs-5" role="tablist">
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
        {tab === "reviews" && <ReviewQueue getToken={getToken} />}
        {tab === "settings" && <OrderSettingsForm getToken={getToken} />}
        {tab === "events" && <ErrandEvents getToken={getToken} />}
        {tab === "flags" && <ActivityFlags getToken={getToken} />}
        {tab === "outbox" && <FailedOutbox getToken={getToken} />}
      </div>
    </>
  );
}
