import { useState } from "react";
import { DEFAULT_CHOICE, PeriodFilter, type PeriodChoice } from "./metrics/PeriodFilter";
import {
  ActiveCouriersPanel,
  ErrandsPerDayPanel,
  OutcomesPanel,
  RewardVolumePanel,
  TimeToAcceptPanel,
  TopBuildingsPanel,
  TopSuppliersPanel,
} from "./metrics/Panels";

interface Props {
  /** A token for the admin-service audience with dashboard:read. */
  getToken: () => Promise<string>;
}

// The Metrics tab. One period filter scopes every panel below it; each panel
// loads on its own, so a failure in one never blanks the others. Days are
// counted in Asia/Singapore.
export function MetricsPage({ getToken }: Props) {
  const [choice, setChoice] = useState<PeriodChoice>(DEFAULT_CHOICE);
  const { period } = choice;

  return (
    <>
      <div className="viz-toolbar">
        <PeriodFilter choice={choice} onChange={setChoice} />
        <p className="muted viz-zone">
          {period.from} to {period.to}, both days included. Days are counted in Singapore time.
        </p>
      </div>

      <div className="viz-layout">
        <ErrandsPerDayPanel getToken={getToken} period={period} />
        <RewardVolumePanel getToken={getToken} period={period} />
        <TimeToAcceptPanel getToken={getToken} period={period} />
        <ActiveCouriersPanel getToken={getToken} period={period} />
        <OutcomesPanel getToken={getToken} period={period} />
        <TopSuppliersPanel getToken={getToken} period={period} />
        <TopBuildingsPanel getToken={getToken} period={period} />
      </div>
    </>
  );
}
