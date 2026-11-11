// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: User-frontend admin page and Panels behavior.
// Author review: I validated correctness, edited for style, and added boundary checks.

import {
  getActiveCouriers,
  getErrandsPerDay,
  getOutcomes,
  getRewardVolume,
  getTimeToAccept,
  getTopBuildings,
  getTopSuppliers,
  type OutcomeStatus,
  type Period,
  type Ranking,
} from "../../../api/admin";
import { creditText } from "../../errands/format";
import { ColumnChart } from "./ColumnChart";
import { formatSeconds, longDate, percentages, shortDate } from "./format";
import { Panel } from "./Panel";
import { SegmentedBar } from "./SegmentedBar";
import { useMetric } from "./useMetric";

interface PanelProps {
  getToken: () => Promise<string>;
  period: Period;
}

function Stat({ label, value, note }: { label: string; value: string; note?: string }) {
  return (
    <div className="viz-stat">
      <span className="viz-stat-label">{label}</span>
      <span className="viz-stat-value">{value}</span>
      {note && <span className="viz-stat-note muted">{note}</span>}
    </div>
  );
}

function Empty({ children }: { children: string }) {
  return <p className="muted viz-empty">{children}</p>;
}

// ---- metric 5 ----------------------------------------------------------------

export function ErrandsPerDayPanel({ getToken, period }: PanelProps) {
  const metric = useMetric(getToken, (token) => getErrandsPerDay(token, period), [period.from, period.to]);
  return (
    <Panel
      title="Errands per day"
      note="Counted from when an errand opened to couriers, not when it was created."
      metric={metric}
      wide
      table={(data) => (
        <table className="viz-table">
          <thead>
            <tr>
              <th scope="col">Day</th>
              <th scope="col">Errands opened</th>
            </tr>
          </thead>
          <tbody>
            {data.days.map((d) => (
              <tr key={d.date}>
                <th scope="row">{longDate(d.date)}</th>
                <td>{d.created.toLocaleString()}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    >
      {(data) => {
        const total = data.days.reduce((sum, d) => sum + d.created, 0);
        return (
          <>
            <p className="viz-total">
              <strong>{total.toLocaleString()}</strong> {total === 1 ? "errand" : "errands"} opened,{" "}
              {shortDate(data.from)} to {shortDate(data.to)}
            </p>
            <ColumnChart
              unit="errands"
              columns={data.days.map((d) => ({ date: d.date, value: d.created }))}
              summary={`Errands opened per day, ${total} in total.`}
            />
          </>
        );
      }}
    </Panel>
  );
}

// ---- metric 6 ----------------------------------------------------------------

export function TimeToAcceptPanel({ getToken, period }: PanelProps) {
  const metric = useMetric(getToken, (token) => getTimeToAccept(token, period), [period.from, period.to]);
  return (
    <Panel
      title="Time to accept"
      note="From opening to the first accept. Errands nobody accepted are left out."
      metric={metric}
    >
      {(data) =>
        data.accepted === 0 ? (
          <Empty>No errands were accepted in this period.</Empty>
        ) : (
          <div className="viz-stats">
            <Stat label="Median" value={formatSeconds(data.medianSeconds ?? 0)} />
            <Stat label="90th percentile" value={formatSeconds(data.p90Seconds ?? 0)} note="nine in ten were faster" />
            <Stat
              label="Accepted errands"
              value={data.accepted.toLocaleString()}
              note="whose first accept fell in the period"
            />
          </div>
        )
      }
    </Panel>
  );
}

// ---- metric 7 ----------------------------------------------------------------

// Each outcome has its own fixed slot, so a status keeps its colour whatever
// the other counts are. The row list beside the bar names every part and gives
// its figures, which is what keeps the lighter hues readable.
const OUTCOMES: Record<OutcomeStatus, { label: string; color: string; meaning: string }> = {
  COMPLETED: { label: "Completed", color: "var(--series-1)", meaning: "Delivered and paid to the courier" },
  CANCELLED: { label: "Cancelled", color: "var(--series-2)", meaning: "Cancelled by the requester" },
  EXPIRED: { label: "Expired", color: "var(--series-3)", meaning: "Passed its acceptance deadline" },
  REJECTED: { label: "Rejected", color: "var(--series-4)", meaning: "Credit reservation refused or timed out" },
  REFUNDED: { label: "Refunded", color: "var(--series-5)", meaning: "Refunded to the requester after a review" },
};

export function OutcomesPanel({ getToken, period }: PanelProps) {
  const metric = useMetric(getToken, (token) => getOutcomes(token, period), [period.from, period.to]);
  return (
    <Panel
      title="How errands ended"
      note="Over errands that finished in the period. Every end state is shown, so the shares add to 100%."
      metric={metric}
    >
      {(data) => {
        const shares = percentages(data.outcomes.map((o) => o.count));
        const segments = data.outcomes.map((o) => ({
          key: o.status,
          label: OUTCOMES[o.status].label,
          count: o.count,
          color: OUTCOMES[o.status].color,
        }));
        if (data.finished === 0) return <Empty>No errands finished in this period.</Empty>;
        return (
          <>
            <p className="viz-total">
              <strong>{data.finished.toLocaleString()}</strong> {data.finished === 1 ? "errand" : "errands"} finished
            </p>
            <SegmentedBar
              segments={segments}
              description={`How ${data.finished} finished errands ended: ${segments
                .map((s, i) => `${s.label} ${s.count} (${shares[i]}%)`)
                .join(", ")}.`}
            />
            <table className="viz-table viz-outcomes">
              <thead className="sr-only">
                <tr>
                  <th scope="col">Outcome</th>
                  <th scope="col">Errands</th>
                  <th scope="col">Share</th>
                </tr>
              </thead>
              <tbody>
                {data.outcomes.map((o, i) => (
                  <tr key={o.status}>
                    <th scope="row">
                      <span className="viz-swatch" style={{ background: OUTCOMES[o.status].color }} aria-hidden />
                      {OUTCOMES[o.status].label}
                      <span className="viz-meaning muted">{OUTCOMES[o.status].meaning}</span>
                    </th>
                    <td>{o.count.toLocaleString()}</td>
                    <td>{shares[i]}%</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </>
        );
      }}
    </Panel>
  );
}

// ---- metrics 8 and 9 ---------------------------------------------------------

function RankedList({ ranking, unnamed, what }: { ranking: Ranking; unnamed: string; what: string }) {
  const top = Math.max(1, ...ranking.items.map((i) => i.finished));
  if (ranking.items.length === 0 && ranking.unattributed === 0) {
    return <Empty>No errands finished in this period.</Empty>;
  }
  return (
    <>
      {ranking.items.length > 0 && (
        <ol className="viz-ranked">
          {ranking.items.map((item, index) => (
            <li key={item.id}>
              <span className="viz-rank-name">
                <span className="viz-rank-n muted">{index + 1}</span>
                {item.name ?? unnamed}
              </span>
              <span className="viz-rank-track" aria-hidden>
                <span className="viz-rank-bar" style={{ width: `${(item.finished / top) * 100}%` }} />
              </span>
              <span className="viz-rank-value">
                <strong>{item.finished.toLocaleString()}</strong> finished
                <span className="muted"> · {item.completed.toLocaleString()} completed</span>
              </span>
            </li>
          ))}
        </ol>
      )}
      {ranking.unattributed > 0 && (
        <p className="muted viz-unattributed">
          {ranking.unattributed.toLocaleString()} finished {ranking.unattributed === 1 ? "errand is" : "errands are"}{" "}
          not ranked yet: the {what} has not been looked up. They appear once it has.
        </p>
      )}
    </>
  );
}

function RankingTable({ ranking, name }: { ranking: Ranking; name: string }) {
  return (
    <table className="viz-table">
      <thead>
        <tr>
          <th scope="col">#</th>
          <th scope="col">{name}</th>
          <th scope="col">Finished</th>
          <th scope="col">Completed</th>
        </tr>
      </thead>
      <tbody>
        {ranking.items.map((item, index) => (
          <tr key={item.id}>
            <td>{index + 1}</td>
            <th scope="row">{item.name ?? "Unnamed"}</th>
            <td>{item.finished.toLocaleString()}</td>
            <td>{item.completed.toLocaleString()}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

export function TopSuppliersPanel({ getToken, period }: PanelProps) {
  const metric = useMetric(getToken, (token) => getTopSuppliers(token, period), [period.from, period.to]);
  return (
    <Panel
      title="Top pickup suppliers"
      note="Where errands are picked up, by errands finished in the period."
      metric={metric}
      table={(data) => <RankingTable ranking={data} name="Pickup supplier" />}
    >
      {(data) => <RankedList ranking={data} unnamed="Unnamed supplier" what="pickup supplier" />}
    </Panel>
  );
}

export function TopBuildingsPanel({ getToken, period }: PanelProps) {
  const metric = useMetric(getToken, (token) => getTopBuildings(token, period), [period.from, period.to]);
  return (
    <Panel
      title="Top delivery buildings"
      note="Where errands are dropped off, by errands finished in the period."
      metric={metric}
      table={(data) => <RankingTable ranking={data} name="Delivery building" />}
    >
      {(data) => <RankedList ranking={data} unnamed="Unnamed building" what="delivery building" />}
    </Panel>
  );
}

// ---- metric 10 ---------------------------------------------------------------

export function ActiveCouriersPanel({ getToken, period }: PanelProps) {
  const metric = useMetric(getToken, (token) => getActiveCouriers(token, period), [period.from, period.to]);
  return (
    <Panel title="Active couriers" note="Couriers who accepted at least one errand in the period." metric={metric}>
      {(data) => (
        <div className="viz-stats">
          <Stat label="Couriers" value={data.couriers.toLocaleString()} />
        </div>
      )}
    </Panel>
  );
}

// ---- metric 11 ---------------------------------------------------------------

export function RewardVolumePanel({ getToken, period }: PanelProps) {
  const metric = useMetric(getToken, (token) => getRewardVolume(token, period), [period.from, period.to]);
  return (
    <Panel
      title="Reward completed per day"
      note="The reward of completed errands, by the day they completed."
      metric={metric}
      wide
      table={(data) => (
        <table className="viz-table">
          <thead>
            <tr>
              <th scope="col">Day</th>
              <th scope="col">Reward</th>
              <th scope="col">Completed errands</th>
            </tr>
          </thead>
          <tbody>
            {data.days.map((d) => (
              <tr key={d.date}>
                <th scope="row">{longDate(d.date)}</th>
                <td>{d.reward.toLocaleString()}</td>
                <td>{d.completed.toLocaleString()}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    >
      {(data) => (
        <>
          <p className="viz-total">
            <strong>{creditText(data.total)}</strong> across {data.completed.toLocaleString()} completed{" "}
            {data.completed === 1 ? "errand" : "errands"}
          </p>
          <ColumnChart
            unit="credits"
            formatValue={(v) => creditText(v)}
            columns={data.days.map((d) => ({ date: d.date, value: d.reward }))}
            summary={`Reward of completed errands per day, ${data.total} credits in total.`}
          />
        </>
      )}
    </Panel>
  );
}
