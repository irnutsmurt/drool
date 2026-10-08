import { useId, useMemo, useRef, useState } from "react";
import { fmtValue, seriesLabel, seriesUnit } from "./format";

export type ChartSeries = { key: string; points: number[][] };
export type ChartGap = { from: number; to: number; reason: string };

// Up to four series per chart; each has its own colour, dash pattern and
// marker, so they stay distinguishable without colour.
const styles = [
  { color: "var(--ui-chart-1)", dash: "", marker: "circle" },
  { color: "var(--ui-chart-2)", dash: "6 3", marker: "square" },
  { color: "var(--ui-chart-3)", dash: "2 3", marker: "triangle" },
  { color: "var(--ui-chart-4)", dash: "10 3 2 3", marker: "diamond" },
] as const;

function Marker({ kind, x, y, color }: { kind: string; x: number; y: number; color: string }) {
  switch (kind) {
    case "square":
      return <rect x={x - 3} y={y - 3} width={6} height={6} fill={color} />;
    case "triangle":
      return <path d={`M${x} ${y - 4} L${x + 4} ${y + 3} L${x - 4} ${y + 3} Z`} fill={color} />;
    case "diamond":
      return <path d={`M${x} ${y - 4} L${x + 4} ${y} L${x} ${y + 4} L${x - 4} ${y} Z`} fill={color} />;
  }
  return <circle cx={x} cy={y} r={3} fill={color} />;
}

const W = 640;
const H = 180;
const PAD = { l: 76, r: 12, t: 10, b: 24 };

function timeLabel(t: number, span: number) {
  const d = new Date(t * 1000);
  return span > 2 * 86400 ? d.toLocaleDateString(undefined, { month: "short", day: "numeric" }) : d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}

/** A time series chart: averages as lines, window maxima as a thin
 * dotted trace, missing data as breaks (and reported gaps shaded), and a
 * table view with the same numbers for keyboard and screen-reader use. */
export function Chart({ title, series, from, to, step, gaps = [], maxPct = true }: {
  title: string;
  series: ChartSeries[];
  from: number;
  to: number;
  step: number;
  gaps?: ChartGap[];
  maxPct?: boolean;
}) {
  const id = useId();
  const [table, setTable] = useState(false);
  const [hover, setHover] = useState<number | null>(null);
  const svg = useRef<SVGSVGElement>(null);
  const shown = series.slice(0, 4);
  const unit = seriesUnit(shown[0]?.key ?? "cpu");
  const top = useMemo(() => {
    if (unit === "pct" && maxPct) return 100;
    let m = 0;
    for (const s of shown) for (const p of s.points) m = Math.max(m, p[2] ?? p[1] ?? 0);
    return m > 0 ? m * 1.1 : 1;
  }, [shown, unit, maxPct]);
  const x = (t: number) => PAD.l + ((t - from) / Math.max(1, to - from)) * (W - PAD.l - PAD.r);
  const y = (v: number) => PAD.t + (1 - v / top) * (H - PAD.t - PAD.b);
  const breakAfter = Math.max(step, 60) * 2.5;
  const path = (pts: number[][], idx: 1 | 2) => {
    let d = "";
    let prev = 0;
    for (const p of pts) {
      const t = p[0]!;
      d += `${d === "" || t - prev > breakAfter ? "M" : "L"}${x(t).toFixed(1)} ${y(p[idx]!).toFixed(1)} `;
      prev = t;
    }
    return d;
  };
  const empty = shown.every((s) => s.points.length === 0);
  const ticks = [0, 0.25, 0.5, 0.75, 1].map((f) => f * top);
  const times = [0, 1, 2, 3, 4].map((i) => from + ((to - from) * i) / 4);
  const nearest = (s: ChartSeries, t: number) => {
    let best: number[] | undefined;
    for (const p of s.points) if (!best || Math.abs(p[0]! - t) < Math.abs(best[0]! - t)) best = p;
    return best && Math.abs(best[0]! - t) <= breakAfter ? best : undefined;
  };
  const onMove = (e: React.MouseEvent) => {
    const r = svg.current?.getBoundingClientRect();
    if (!r) return;
    const px = ((e.clientX - r.left) / r.width) * W;
    setHover(from + ((px - PAD.l) / (W - PAD.l - PAD.r)) * (to - from));
  };
  return (
    <figure className="min-w-0" aria-labelledby={`${id}-cap`}>
      <figcaption id={`${id}-cap`} className="mb-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-sm">
        <span className="font-medium">{title}</span>
        <ul className="flex flex-wrap gap-x-3 gap-y-1 text-xs text-muted">
          {shown.map((s, i) => (
            <li key={s.key} className="inline-flex items-center gap-1">
              <svg width="26" height="10" aria-hidden="true">
                <line x1="1" y1="5" x2="25" y2="5" stroke={styles[i]!.color} strokeWidth="2" strokeDasharray={styles[i]!.dash} />
                <Marker kind={styles[i]!.marker} x={13} y={5} color={styles[i]!.color} />
              </svg>
              {seriesLabel(s.key)}
            </li>
          ))}
          {!empty && <li className="inline-flex items-center gap-1"><svg width="18" height="10" aria-hidden="true"><line x1="1" y1="5" x2="17" y2="5" stroke="currentColor" strokeWidth="1" strokeDasharray="1 2" /></svg>window maximum</li>}
        </ul>
        <button type="button" className="ml-auto text-xs text-accent underline-offset-2 hover:underline" aria-expanded={table}
          onClick={() => setTable((v) => !v)}>
          {table ? "Show chart" : "Show as table"}
        </button>
      </figcaption>
      {empty ? (
        <p className="rounded-ui border border-dashed border-line px-3 py-6 text-center text-sm text-muted">No data in this range yet.</p>
      ) : table ? (
        <div className="max-h-64 overflow-auto rounded-ui border border-line">
          <table className="w-full text-left text-xs">
            <caption className="sr-only">{title}: average and maximum per window</caption>
            <thead className="sticky top-0 bg-surface">
              <tr className="border-b border-line text-muted">
                <th scope="col" className="px-2 py-1 font-medium">Time</th>
                {shown.map((s) => <th key={s.key} scope="col" className="px-2 py-1 font-medium">{seriesLabel(s.key)} (avg / max)</th>)}
              </tr>
            </thead>
            <tbody>
              {[...new Set(shown.flatMap((s) => s.points.map((p) => p[0]!)))].sort((a, b) => b - a).map((t) => (
                <tr key={t} className="border-b border-line last:border-0">
                  <th scope="row" className="px-2 py-1 font-normal text-muted">{new Date(t * 1000).toLocaleString()}</th>
                  {shown.map((s) => {
                    const p = s.points.find((q) => q[0] === t);
                    return <td key={s.key} className="px-2 py-1 tabular-nums">{p ? `${fmtValue(unit, p[1])} / ${fmtValue(unit, p[2])}` : "no data"}</td>;
                  })}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <div className="relative">
          <svg ref={svg} viewBox={`0 0 ${W} ${H}`} className="h-auto w-full" role="img" aria-label={`${title} chart; use "Show as table" for the values`}
            onMouseMove={onMove} onMouseLeave={() => setHover(null)}>
            <defs>
              <pattern id={`${id}-gap`} width="6" height="6" patternUnits="userSpaceOnUse" patternTransform="rotate(45)">
                <line x1="0" y1="0" x2="0" y2="6" stroke="var(--ui-muted)" strokeWidth="1" opacity="0.4" />
              </pattern>
            </defs>
            {ticks.map((v) => (
              <g key={v}>
                <line x1={PAD.l} x2={W - PAD.r} y1={y(v)} y2={y(v)} stroke="var(--ui-chart-grid)" />
                <text x={PAD.l - 6} y={y(v) + 3} textAnchor="end" fontSize="10" fill="var(--ui-muted)">{fmtValue(unit, v)}</text>
              </g>
            ))}
            {times.map((t, i) => (
              <text key={t} x={x(t)} y={H - 6} textAnchor={i === 0 ? "start" : i === times.length - 1 ? "end" : "middle"} fontSize="10" fill="var(--ui-muted)">{timeLabel(t, to - from)}</text>
            ))}
            {gaps.filter((g) => g.to > from && g.from < to).map((g) => (
              <g key={`${g.from}-${g.to}`}>
                <rect x={x(Math.max(g.from, from))} y={PAD.t} width={Math.max(2, x(Math.min(g.to, to)) - x(Math.max(g.from, from)))} height={H - PAD.t - PAD.b}
                  fill={`url(#${id}-gap)`}><title>Telemetry gap: {g.reason}</title></rect>
                <text x={x(Math.max(g.from, from)) + 2} y={PAD.t + 10} fontSize="9" fill="var(--ui-muted)">gap</text>
              </g>
            ))}
            {shown.map((s, i) => (
              <g key={s.key}>
                <path d={path(s.points, 2)} fill="none" stroke={styles[i]!.color} strokeWidth="1" strokeDasharray="1 2" opacity="0.8" />
                <path d={path(s.points, 1)} fill="none" stroke={styles[i]!.color} strokeWidth="2" strokeDasharray={styles[i]!.dash} />
                {s.points.length <= 40 && s.points.map((p) => <Marker key={p[0]} kind={styles[i]!.marker} x={x(p[0]!)} y={y(p[1]!)} color={styles[i]!.color} />)}
              </g>
            ))}
            {hover != null && hover >= from && hover <= to && (
              <line x1={x(hover)} x2={x(hover)} y1={PAD.t} y2={H - PAD.b} stroke="var(--ui-muted)" strokeDasharray="2 2" />
            )}
          </svg>
          {hover != null && hover >= from && hover <= to && (
            <div className="pointer-events-none absolute right-2 top-2 rounded-ui border border-line bg-surface px-2 py-1 text-xs shadow">
              <div className="text-muted">{new Date(hover * 1000).toLocaleString()}</div>
              {shown.map((s) => {
                const p = nearest(s, hover);
                return <div key={s.key}>{seriesLabel(s.key)}: {p ? `${fmtValue(unit, p[1])} (max ${fmtValue(unit, p[2])})` : "no data"}</div>;
              })}
            </div>
          )}
        </div>
      )}
    </figure>
  );
}
