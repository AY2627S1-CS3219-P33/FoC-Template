import { useCallback } from "react";
import {
  listDeliveringErrands,
  listErrandHistory,
  listRequestedErrands,
  type ParticipantErrandView,
} from "../../api/order";
import { ErrandCard } from "./ErrandCard";
import { usePagedList } from "./usePagedList";
import { errandError } from "./messages";
import { formatDateTime, timeLeft } from "./format";

export type MyListKind = "requested" | "delivering" | "history";

interface Props {
  kind: MyListKind;
  getToken: () => Promise<string>;
  onOpen: (errandId: string) => void;
  onCreate: () => void;
}

const PAGE_SIZE = 12;

const EMPTY: Record<MyListKind, { title: string; hint: string }> = {
  requested: {
    title: "You have no errands in progress.",
    hint: "Request one and a courier can pick it up.",
  },
  delivering: {
    title: "You are not delivering anything right now.",
    hint: "Accept an open errand from Browse to start.",
  },
  history: {
    title: "Nothing finished yet.",
    hint: "Completed, cancelled and expired errands collect here.",
  },
};

// What matters about an errand depends on where it is: a courier watches the
// pickup and delivery deadlines, a requester watches the verification window,
// and a finished errand just says when it ended.
function footnote(errand: ParticipantErrandView): string {
  switch (errand.status) {
    case "PENDING_RESERVATION":
      return "Waiting for credits to be reserved";
    case "OPEN":
      return `Acceptance closes ${timeLeft(errand.acceptanceDeadline)}`;
    case "ASSIGNED":
      return errand.pickupDeadline
        ? `Pick up ${timeLeft(errand.pickupDeadline)}`
        : "Assigned to a courier";
    case "PICKED_UP":
      return errand.deliveryDeadline
        ? `Deliver ${timeLeft(errand.deliveryDeadline)}`
        : "On the way";
    case "DELIVERED":
      return errand.verificationDeadline
        ? `Auto-completes ${timeLeft(errand.verificationDeadline)}`
        : "Delivered";
    case "REVIEW_REQUIRED":
      return "Waiting for an administrator";
    default:
      return `Ended ${formatDateTime(errand.updatedAt)}`;
  }
}

export function MyErrandList({ kind, getToken, onOpen, onCreate }: Props) {
  const load = useCallback(
    async (cursor?: string) => {
      const token = await getToken();
      const params = { limit: PAGE_SIZE, cursor };
      if (kind === "requested") return listRequestedErrands(token, params);
      if (kind === "delivering") return listDeliveringErrands(token, params);
      return listErrandHistory(token, params);
    },
    [getToken, kind],
  );

  const list = usePagedList<ParticipantErrandView>(load, errandError);
  const empty = EMPTY[kind];

  return (
    <>
      <div className="toolbar">
        <button type="button" className="btn outline slim" onClick={list.reload} disabled={list.loading}>
          Refresh
        </button>
      </div>

      {list.error && (
        <div className="banner banner-error" role="alert">
          <span>{list.error}</span>
        </div>
      )}

      {!list.error && !list.loading && list.items.length === 0 && (
        <div className="placeholder">
          <p>{empty.title}</p>
          <p className="muted">{empty.hint}</p>
          {kind === "requested" && (
            <button type="button" className="btn slim mt" onClick={onCreate}>
              Request an errand
            </button>
          )}
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
            status={errand.status}
            footnote={footnote(errand)}
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
