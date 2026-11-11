// AI Assistance Disclosure:
// Tool: Claude, date: 2026-11-11
// Scope: User-frontend admin page and SegmentedBar behavior.
// Author review: I validated correctness, edited for style, and added boundary checks.

export interface Segment {
  key: string;
  label: string;
  count: number;
  /** A CSS colour, normally a --series-N token. */
  color: string;
}

interface Props {
  segments: Segment[];
  /** The accessible name: every part's label and count, since the bar prints none. */
  description: string;
}

// One whole split into parts, read beside a list that names each part and
// gives its figures, so the bar carries no text: thin, fully rounded ends,
// a 2px surface gap between parts, widths proportional to the counts. A part
// at zero draws nothing.
export function SegmentedBar({ segments, description }: Props) {
  const shown = segments.filter((s) => s.count > 0);
  return (
    <div className="viz-segbar" role="img" aria-label={description}>
      {shown.length === 0 ? (
        <span className="viz-segbar-empty" />
      ) : (
        shown.map((s) => (
          <span key={s.key} className="viz-seg" style={{ flexGrow: s.count, background: s.color }} />
        ))
      )}
    </div>
  );
}
