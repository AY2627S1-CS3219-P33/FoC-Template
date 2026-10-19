import type { ErrandStatus, Item, PickupLocation, DeliveryBuilding } from "../../api/order";
import {
  creditText,
  formatDistance,
  itemCount,
  itemSummary,
  statusClass,
  statusLabel,
  timeLeft,
} from "./format";

// The fields every errand projection shares, so one card renders an open-list
// item, a requested errand and a history row alike.
interface Props {
  pickup: PickupLocation;
  deliveryBuilding: DeliveryBuilding;
  deliveryDetail: string;
  items: Item[];
  reward: number;
  acceptanceDeadline: string;
  // Absent on the open list, which has no status: every row there is OPEN.
  status?: ErrandStatus;
  distanceMeters?: number;
  // Shown instead of the acceptance countdown once the errand is no longer
  // waiting to be accepted.
  footnote?: string;
  onOpen: () => void;
}

export function ErrandCard(props: Props) {
  const { pickup, deliveryBuilding, items, reward, status, distanceMeters } = props;
  const count = itemCount(items);

  return (
    <button type="button" className="errand-card" onClick={props.onOpen}>
      <div className="errand-card-head">
        <h3>{pickup.name}</h3>
        {status ? (
          <span className={statusClass(status)}>{statusLabel(status)}</span>
        ) : (
          <span className="reward-chip">{creditText(reward)}</span>
        )}
      </div>

      <p className="errand-route">
        <span className="errand-leg">
          {pickup.building} · Floor {pickup.floor}
        </span>
        <span className="errand-arrow" aria-hidden>
          →
        </span>
        <span className="errand-leg">{deliveryBuilding.name}</span>
      </p>

      <p className="errand-items" title={itemSummary(items)}>
        {count} {count === 1 ? "item" : "items"} · {itemSummary(items)}
      </p>

      <p className="errand-detail">{props.deliveryDetail}</p>

      <div className="errand-card-foot">
        {status && <span className="reward-chip">{creditText(reward)}</span>}
        {props.footnote ? (
          <span className="muted small">{props.footnote}</span>
        ) : (
          <span className="muted small">Accept {timeLeft(props.acceptanceDeadline)}</span>
        )}
        {distanceMeters !== undefined && (
          <span className="distance-chip">{formatDistance(distanceMeters)}</span>
        )}
      </div>
    </button>
  );
}
