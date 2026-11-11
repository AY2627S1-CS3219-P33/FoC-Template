// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: User-frontend admin page and Panel behavior.
// Author review: I validated correctness, edited for style, and added boundary checks.

import { useState, type ReactNode } from "react";
import { formatDateTime } from "../../errands/format";
import type { Metric } from "./useMetric";

interface Props<T extends { asOf: string }> {
  title: string;
  /** One line under the title saying what is counted. */
  note?: string;
  metric: Metric<T>;
  /** The chart or figures. Called only when there is data. */
  children: (data: T) => ReactNode;
  /** The same numbers as a table: the chart's accessible twin. */
  table?: (data: T) => ReactNode;
  wide?: boolean;
  /** Extra controls beside the title, such as a refresh button. */
  action?: ReactNode;
}

// A metric's frame. Each panel owns its load error and its asOf, so one
// failing request never blanks the page. While data reloads, the previous
// render stays in view, dimmed, instead of flashing a skeleton.
export function Panel<T extends { asOf: string }>({ title, note, metric, children, table, wide, action }: Props<T>) {
  const [showTable, setShowTable] = useState(false);
  const { data, error, loading, reload } = metric;

  return (
    <section className={`viz-panel${wide ? " is-wide" : ""}`} aria-busy={loading}>
      <header className="viz-panel-head">
        <div>
          <h2>{title}</h2>
          {note && <p className="muted">{note}</p>}
        </div>
        {action}
        {data && table && (
          <button
            type="button"
            className="btn outline slim"
            aria-pressed={showTable}
            onClick={() => setShowTable((v) => !v)}
          >
            {showTable ? "Chart" : "Table"}
          </button>
        )}
      </header>

      {error && (
        <div className="banner banner-error" role="alert">
          <span>{error}</span>
          <button type="button" className="link-btn" onClick={reload} disabled={loading}>
            Try again
          </button>
        </div>
      )}

      {data ? (
        <div className={`viz-body${loading ? " is-refreshing" : ""}`}>
          {showTable && table ? table(data) : children(data)}
        </div>
      ) : (
        !error && <p className="muted viz-loading">Loading…</p>
      )}

      {data && (
        <footer className="viz-asof muted">
          As of {formatDateTime(data.asOf)}
          {error ? " (the last successful load)" : ""}
        </footer>
      )}
    </section>
  );
}
