// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: User-frontend admin page and PeriodFilter behavior.
// Author review: I validated correctness, edited for style, and added boundary checks.

import { useState } from "react";
import { addDays, lastDays, today, type Period } from "../../../api/admin";

export type Preset = 7 | 30 | 90 | "custom";

export interface PeriodChoice {
  preset: Preset;
  period: Period;
}

export const DEFAULT_CHOICE: PeriodChoice = { preset: 30, period: lastDays(30) };

const PRESETS: { value: 7 | 30 | 90; label: string }[] = [
  { value: 7, label: "Last 7 days" },
  { value: 30, label: "Last 30 days" },
  { value: 90, label: "Last 90 days" },
];

const MAX_DAYS = 366; // admin-service's limit

function daysBetween(from: string, to: string): number {
  return Math.round((Date.parse(`${to}T00:00:00Z`) - Date.parse(`${from}T00:00:00Z`)) / 86_400_000) + 1;
}

// The one filter row above everything it scopes: presets first, then a custom
// range. Both ends are included, in the dashboard's time zone.
export function PeriodFilter({ choice, onChange }: { choice: PeriodChoice; onChange: (next: PeriodChoice) => void }) {
  const [from, setFrom] = useState(choice.period.from);
  const [to, setTo] = useState(choice.period.to);
  const custom = choice.preset === "custom";
  const latest = today();

  let problem: string | null = null;
  if (custom) {
    if (!from || !to) problem = "Choose both days.";
    else if (from > to) problem = "The first day must not be after the last.";
    else if (daysBetween(from, to) > MAX_DAYS) problem = `A period is at most ${MAX_DAYS} days.`;
  }

  const applyCustom = (nextFrom: string, nextTo: string) => {
    if (nextFrom && nextTo && nextFrom <= nextTo && daysBetween(nextFrom, nextTo) <= MAX_DAYS) {
      onChange({ preset: "custom", period: { from: nextFrom, to: nextTo } });
    }
  };

  return (
    <div className="viz-filter" role="group" aria-label="Period">
      {PRESETS.map((p) => (
        <button
          key={p.value}
          type="button"
          className="viz-chip"
          aria-pressed={choice.preset === p.value}
          onClick={() => {
            const next = { from: addDays(today(), -(p.value - 1)), to: today() };
            setFrom(next.from);
            setTo(next.to);
            onChange({ preset: p.value, period: next });
          }}
        >
          {p.label}
        </button>
      ))}
      <button
        type="button"
        className="viz-chip"
        aria-pressed={custom}
        onClick={() => onChange({ preset: "custom", period: { from, to } })}
      >
        Custom
      </button>
      {custom && (
        <span className="viz-dates">
          <label>
            <span className="sr-only">First day</span>
            <input
              type="date"
              className="input"
              value={from}
              max={to || latest}
              onChange={(e) => {
                setFrom(e.target.value);
                applyCustom(e.target.value, to);
              }}
            />
          </label>
          <span aria-hidden>to</span>
          <label>
            <span className="sr-only">Last day</span>
            <input
              type="date"
              className="input"
              value={to}
              min={from}
              onChange={(e) => {
                setTo(e.target.value);
                applyCustom(from, e.target.value);
              }}
            />
          </label>
          {problem && (
            <span className="viz-filter-problem" role="alert">
              {problem}
            </span>
          )}
        </span>
      )}
    </div>
  );
}
