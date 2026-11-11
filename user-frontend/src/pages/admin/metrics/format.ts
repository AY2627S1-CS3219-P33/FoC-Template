// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: User-frontend admin page and format behavior.
// Author review: I validated correctness, edited for style, and added boundary checks.

// Number, duration and date formatting for the metric panels.

/** "45 s", "1 min 36 s", "2 h 5 min". Coarse on purpose: these are medians. */
export function formatSeconds(totalSeconds: number): string {
  const seconds = Math.max(0, Math.round(totalSeconds));
  if (seconds < 60) return `${seconds} s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) {
    const rest = seconds % 60;
    return rest === 0 ? `${minutes} min` : `${minutes} min ${rest} s`;
  }
  const hours = Math.floor(minutes / 60);
  const restMinutes = minutes % 60;
  if (hours < 24) return restMinutes === 0 ? `${hours} h` : `${hours} h ${restMinutes} min`;
  const days = Math.floor(hours / 24);
  const restHours = hours % 24;
  return restHours === 0 ? `${days} d` : `${days} d ${restHours} h`;
}

/**
 * Whole percentages that add to exactly 100 (largest-remainder rounding), so
 * five rounded shares never read as 99% or 101%. All zeros when nothing counts.
 */
export function percentages(counts: readonly number[]): number[] {
  const total = counts.reduce((sum, n) => sum + n, 0);
  if (total === 0) return counts.map(() => 0);
  const exact = counts.map((n) => (n / total) * 100);
  const floors = exact.map(Math.floor);
  let remaining = 100 - floors.reduce((sum, n) => sum + n, 0);
  const byRemainder = exact
    .map((value, index) => ({ index, remainder: value - Math.floor(value) }))
    // Ties go to the larger count, then the earlier row, so the result is stable.
    .sort((a, b) => b.remainder - a.remainder || counts[b.index] - counts[a.index] || a.index - b.index);
  for (const { index } of byRemainder) {
    if (remaining === 0) break;
    floors[index] += 1;
    remaining -= 1;
  }
  return floors;
}

const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

/** "2026-03-02" → "2 Mar". The date is a calendar date, so no zone applies. */
export function shortDate(date: string): string {
  const [, month, day] = date.split("-").map(Number);
  return `${day} ${MONTHS[month - 1] ?? "?"}`;
}

/** "2026-03-02" → "2 Mar 2026". */
export function longDate(date: string): string {
  const [year] = date.split("-");
  return `${shortDate(date)} ${year}`;
}

/** A tick step (1, 2, 5, 10, 20, 50, …) giving at most `maxTicks` gridlines. */
export function tickStep(max: number, maxTicks = 4): number {
  if (max <= 0) return 1;
  let step = 1;
  while (max / step > maxTicks) {
    // 1 → 2 → 5 → 10 → 20 → 50 …
    const magnitude = 10 ** Math.floor(Math.log10(step));
    const lead = Math.round(step / magnitude);
    step = lead === 1 ? 2 * magnitude : lead === 2 ? 5 * magnitude : 10 * magnitude;
  }
  return step;
}

/** The axis labels' compact form: 1,200 → "1.2K". Whole numbers under 10,000. */
export function compact(value: number): string {
  if (Math.abs(value) < 10_000) return value.toLocaleString();
  if (Math.abs(value) < 1_000_000) return `${+(value / 1000).toFixed(1)}K`;
  return `${+(value / 1_000_000).toFixed(1)}M`;
}
