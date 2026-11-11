// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: User-frontend admin page and ColumnChart behavior.
// Author review: I validated correctness, edited for style, and added boundary checks.

import { useEffect, useId, useRef, useState, type KeyboardEvent } from "react";
import { compact, shortDate, tickStep } from "./format";

export interface Column {
  /** The day, YYYY-MM-DD. */
  date: string;
  value: number;
}

interface Props {
  columns: Column[];
  /** What the values are, for the tooltip and the accessible name: "errands", "credits". */
  unit: string;
  /** How a value reads in the tooltip. */
  formatValue?: (value: number) => string;
  /** The series' accessible summary, ahead of the table view. */
  summary: string;
}

// Geometry in pixels. The width follows the container, so the text keeps its
// size on a phone instead of shrinking with a scaled drawing.
const DEFAULT_WIDTH = 720;
const MIN_WIDTH = 260;
const HEIGHT = 200;
const MIN_LABEL_SLOT = 56; // room one x-axis label needs
const AXIS_LEFT = 40;
const AXIS_BOTTOM = 26;
const TOP = 10;
const RIGHT = 6;
const MAX_BAR = 24; // columns are never fatter than this
const RADIUS = 4;

// One column per day, from a single baseline: thin marks with a rounded
// data-end, hairline gridlines, one series, one colour. The pointer finds the
// day (each column's hit area is its whole slot, not just the painted bar),
// and so do the arrow keys; the Table view beside it carries every value.
export function ColumnChart({ columns, unit, formatValue = (v) => v.toLocaleString(), summary }: Props) {
  const [active, setActive] = useState<number | null>(null);
  const tooltipId = useId();
  const plot = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(DEFAULT_WIDTH);

  useEffect(() => {
    const element = plot.current;
    if (!element || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(([entry]) => {
      setWidth(Math.max(MIN_WIDTH, Math.floor(entry.contentRect.width)));
    });
    observer.observe(element);
    return () => observer.disconnect();
  }, []);

  const plotWidth = width - AXIS_LEFT - RIGHT;
  const plotHeight = HEIGHT - TOP - AXIS_BOTTOM;
  const max = Math.max(0, ...columns.map((c) => c.value));
  const step = tickStep(max);
  const top = Math.max(step, Math.ceil(max / step) * step);
  const ticks: number[] = [];
  for (let v = 0; v <= top; v += step) ticks.push(v);
  const slot = columns.length > 0 ? plotWidth / columns.length : plotWidth;
  const barWidth = Math.min(MAX_BAR, Math.max(1, slot * 0.7));
  const y = (value: number) => TOP + plotHeight - (value / top) * plotHeight;
  const labelEvery = Math.max(1, Math.ceil(MIN_LABEL_SLOT / slot));

  const move = (delta: number) =>
    setActive((current) => {
      if (columns.length === 0) return null;
      const from = current ?? (delta > 0 ? -1 : columns.length);
      return Math.min(columns.length - 1, Math.max(0, from + delta));
    });
  const onKeyDown = (event: KeyboardEvent) => {
    switch (event.key) {
      case "ArrowRight":
        move(1);
        break;
      case "ArrowLeft":
        move(-1);
        break;
      case "Home":
        setActive(columns.length > 0 ? 0 : null);
        break;
      case "End":
        setActive(columns.length > 0 ? columns.length - 1 : null);
        break;
      case "Escape":
        setActive(null);
        return;
      default:
        return;
    }
    event.preventDefault();
  };

  const activeColumn = active === null ? undefined : columns[active];
  const activeX = active === null ? 0 : AXIS_LEFT + slot * active + slot / 2;

  return (
    <div className="viz-frame">
      <div
        className="viz-plot"
        ref={plot}
        tabIndex={0}
        role="group"
        aria-label={`${summary} Use the arrow keys to move between days.`}
        aria-describedby={activeColumn ? tooltipId : undefined}
        onKeyDown={onKeyDown}
        onBlur={() => setActive(null)}
        onPointerLeave={() => setActive(null)}
      >
        <svg width={width} height={HEIGHT} viewBox={`0 0 ${width} ${HEIGHT}`} className="viz-svg" aria-hidden>
          {ticks.map((tick) => (
            <g key={tick}>
              <line
                className={tick === 0 ? "viz-baseline" : "viz-grid"}
                x1={AXIS_LEFT}
                x2={width - RIGHT}
                y1={y(tick)}
                y2={y(tick)}
              />
              <text className="viz-tick" x={AXIS_LEFT - 6} y={y(tick)} textAnchor="end" dominantBaseline="middle">
                {compact(tick)}
              </text>
            </g>
          ))}
          {columns.map((column, index) => {
            const x = AXIS_LEFT + slot * index + (slot - barWidth) / 2;
            const height = (column.value / top) * plotHeight;
            const r = Math.min(RADIUS, height / 2, barWidth / 2);
            return (
              <g key={column.date}>
                {column.value > 0 && (
                  // Rounded at the data end, square on the baseline.
                  <path
                    className={`viz-bar${active === index ? " is-active" : ""}`}
                    d={`M${x},${y(0)} V${y(column.value) + r} a${r},${r} 0 0 1 ${r},${-r} h${barWidth - 2 * r} a${r},${r} 0 0 1 ${r},${r} V${y(0)} Z`}
                  />
                )}
                {index % labelEvery === 0 && (
                  <text className="viz-tick" x={AXIS_LEFT + slot * index + slot / 2} y={HEIGHT - 8} textAnchor="middle">
                    {shortDate(column.date)}
                  </text>
                )}
                <rect
                  className="viz-hit"
                  x={AXIS_LEFT + slot * index}
                  y={TOP}
                  width={slot}
                  height={plotHeight}
                  onPointerEnter={() => setActive(index)}
                  onPointerMove={() => setActive(index)}
                />
              </g>
            );
          })}
        </svg>
        {activeColumn && (
          <div
            id={tooltipId}
            role="status"
            className="viz-tooltip"
            // Kept inside the frame near either edge.
            style={{ left: `clamp(4.5rem, ${(activeX / width) * 100}%, calc(100% - 4.5rem))` }}
          >
            <strong>{formatValue(activeColumn.value)}</strong>
            <span>
              {unit} · {shortDate(activeColumn.date)}
            </span>
          </div>
        )}
      </div>
    </div>
  );
}
