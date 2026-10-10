import { SubjectHealthButton } from "@/components/health/HostHealth";
import { useState } from "react";
import { Card } from "@/components/ui/Card";
import { Chart } from "@/components/monitoring/Chart";
import { useHistory, type MonHostOverview } from "@/components/monitoring/queries";
import { fmtBitRate, fmtByteRate, fmtPct, fmtTemp, seriesUnit } from "@/components/monitoring/format";
import { cx } from "@/components/ui/cx";
import { formatBytes, formatUptime } from "@/lib/format";

const ranges = [
  { key: "1h", label: "Hour" },
  { key: "6h", label: "6 hours" },
  { key: "24h", label: "24 hours" },
  { key: "7d", label: "7 days" },
  { key: "30d", label: "30 days" },
];

/** One request, several charts (series split by unit, four per chart). */
function ChartGroup({ id, range, title, selectors, max = 8 }: { id: string; range: string; title: (key: string, i: number) => string; selectors: string[]; max?: number }) {
  const h = useHistory(id, range, selectors);
  if (!h.data) return <p className="text-sm text-muted">Loading…</p>;
  // Selector order, not alphabetical (load1, load5, load15).
  const rank = (k: string) => {
    const i = selectors.findIndex((sel) => sel === k || (sel.endsWith(":") && k.startsWith(sel)));
    return i < 0 ? selectors.length : i;
  };
  const series = [...h.data.series].sort((a, b) => rank(a.key) - rank(b.key) || a.key.localeCompare(b.key));
  const groups: { unit: string; keys: string[] }[] = [];
  for (const s of series.slice(0, max * 4)) {
    const u = seriesUnit(s.key);
    let g = groups.find((x) => x.unit === u && x.keys.length < 4);
    if (!g) groups.push((g = { unit: u, keys: [] }));
    g.keys.push(s.key);
  }
  if (groups.length === 0) {
    return <p className="text-sm text-muted">No data for this range yet.</p>;
  }
  return (
    <div className="grid gap-6 lg:grid-cols-2">
      {groups.slice(0, max).map((g, i) => (
        <Chart key={g.keys.join()} title={title(g.keys[0]!, i)} from={h.data.from} to={h.data.to} step={h.data.step_s} gaps={h.data.gaps}
          series={g.keys.map((k) => series.find((s) => s.key === k)!)} />
      ))}
      {h.data.truncated && <p className="text-xs text-muted">Only the first series are shown.</p>}
    </div>
  );
}

export function MetricsTab({ id, o, compact }: { id: string; o: MonHostOverview; compact?: boolean }) {
  const [range, setRange] = useState(compact ? "1h" : "24h");
  const st = o.state;
  const phys = (st?.interfaces ?? []).filter((i) => i.kind === "physical");
  return (
    <div className="space-y-4">
      <div role="group" aria-label="Time range" className="flex flex-wrap gap-1">
        {ranges.map((r) => (
          <button key={r.key} type="button" aria-pressed={range === r.key} onClick={() => setRange(r.key)}
            className={cx("rounded-ui border px-2.5 py-1 text-sm", range === r.key ? "border-accent bg-accent/15 font-medium" : "border-line hover:bg-surface-2")}>
            {r.label}
          </button>
        ))}
        <span className="ml-2 self-center text-xs text-muted">
          {range === "1h" || range === "6h" ? "Raw points" : range === "24h" ? "5-minute averages and maxima" : "Hourly averages and maxima"}
        </span>
      </div>
      <Card title="CPU and memory">
        <div className="grid gap-6 lg:grid-cols-2">
          <ChartGroup id={id} range={range} selectors={["cpu", "cpu.iowait"]} title={() => "CPU"} max={1} />
          <ChartGroup id={id} range={range} selectors={["mem", "swap"]} title={() => "Memory and swap"} max={1} />
        </div>
        {!compact && st && (
          <dl className="mt-4 grid grid-cols-2 gap-x-6 gap-y-1 text-sm sm:grid-cols-4">
            <dt className="text-muted">CPUs</dt><dd>{st.cpu?.count ?? "—"}</dd>
            <dt className="text-muted">Load (1/5/15 min)</dt><dd>{st.load ? st.load.map((l) => l.toFixed(2)).join(" / ") : "—"}</dd>
            <dt className="text-muted">Available memory</dt><dd>{st.memory ? `${formatBytes(st.memory.available_bytes)} of ${formatBytes(st.memory.total_bytes)}` : "—"}</dd>
            <dt className="text-muted">Swap used</dt><dd>{fmtPct(st.memory?.swap_percent)}</dd>
            <dt className="text-muted">Uptime</dt><dd>{formatUptime(st.uptime_s)}</dd>
            <dt className="text-muted">CPU pressure (10 s)</dt><dd>{fmtPct(st.pressure?.cpu10, 1)}</dd>
            <dt className="text-muted">Memory pressure (10 s)</dt><dd>{fmtPct(st.pressure?.memory10, 1)}</dd>
            <dt className="text-muted">IO pressure (10 s)</dt><dd>{fmtPct(st.pressure?.io10, 1)}</dd>
          </dl>
        )}
        {!compact && st?.cpu?.cores && st.cpu.cores.length > 1 && (
          <details className="mt-3 text-sm">
            <summary className="cursor-pointer text-accent">Per-core usage now ({st.cpu.cores.length} cores)</summary>
            <ul className="mt-2 grid grid-cols-4 gap-1 sm:grid-cols-8">
              {st.cpu.cores.map((c, i) => <li key={i} className="rounded border border-line px-1.5 py-0.5 text-xs tabular-nums">cpu{i}: {fmtPct(c)}</li>)}
            </ul>
          </details>
        )}
      </Card>
      {!compact && (
        <>
          <Card title="Load">
            <ChartGroup id={id} range={range} selectors={["load1", "load5", "load15"]} title={() => "Load average"} max={1} />
          </Card>
          <Card title="Filesystems">
            <ChartGroup id={id} range={range} selectors={["fs:"]} title={(_, i) => `Space used${i ? ` (${i + 1})` : ""}`} />
            <table className="rtable mt-4 w-full text-left text-sm">
              <caption className="sr-only">Filesystems now</caption>
              <thead><tr className="border-b border-line text-xs text-muted">
                <th className="py-1 font-medium">Mount</th><th className="py-1 font-medium">Device</th><th className="py-1 font-medium">Used</th>
                <th className="py-1 font-medium">Free</th><th className="py-1 font-medium">Inodes</th><th className="py-1 font-medium">State</th>
                <th className="py-1"><span className="sr-only">Health settings</span></th>
              </tr></thead>
              <tbody>
                {(st?.filesystems ?? []).map((f) => (
                  <tr key={f.mount} className="border-b border-line last:border-0">
                    <td data-primary="" className="py-1 font-mono text-xs">{f.mount}</td>
                    <td data-label="Device" className="py-1 text-xs text-muted">{f.device} ({f.type})</td>
                    <td data-label="Used" className="py-1 tabular-nums">{fmtPct(f.used_percent)}</td>
                    <td data-label="Free" className="py-1 tabular-nums">{formatBytes(f.avail_bytes)} of {formatBytes(f.total_bytes)}</td>
                    <td data-label="Inodes" className="py-1 tabular-nums">{fmtPct(f.inodes_percent)}</td>
                    <td data-label="State" className="py-1">{f.unresponsive ? "⚠ not responding" : f.read_only ? "Read-only" : "Read/write"}</td>
                    <td data-label="" className="py-1 text-right"><SubjectHealthButton hostId={id} kind="filesystem" subjectRef={f.mount} label={f.mount} /></td>
                  </tr>
                ))}
              </tbody>
            </table>
          </Card>
          <Card title="Network">
            <ChartGroup id={id} range={range} selectors={phys.length ? phys.flatMap((i) => [`net.rx:${i.name}`, `net.tx:${i.name}`]) : ["net.rx:", "net.tx:"]}
              title={(k) => `Throughput${k.includes(":") ? ` · ${k.slice(k.indexOf(":") + 1)}` : ""}`} max={4} />
            <table className="rtable mt-4 w-full text-left text-sm">
              <caption className="sr-only">Interfaces now</caption>
              <thead><tr className="border-b border-line text-xs text-muted">
                <th className="py-1 font-medium">Interface</th><th className="py-1 font-medium">State</th><th className="py-1 font-medium">Speed</th>
                <th className="py-1 font-medium">Receive</th><th className="py-1 font-medium">Send</th><th className="py-1 font-medium">Errors / drops (total)</th>
              </tr></thead>
              <tbody>
                {(st?.interfaces ?? []).filter((i) => i.kind !== "loopback").slice(0, 32).map((i) => (
                  <tr key={i.name} className="border-b border-line last:border-0">
                    <td data-primary="" className="py-1 font-mono text-xs">{i.name} <span className="text-muted">({i.kind})</span></td>
                    <td data-label="State" className="py-1">{i.state === "up" ? "✓ up" : i.state === "down" ? "✕ down" : i.state}</td>
                    <td data-label="Speed" className="py-1">{i.speed_mbps ? `${i.speed_mbps} Mbit/s` : "—"}</td>
                    <td data-label="Receive" className="py-1 tabular-nums">{fmtBitRate(i.rx_bps)}</td>
                    <td data-label="Send" className="py-1 tabular-nums">{fmtBitRate(i.tx_bps)}</td>
                    <td data-label="Errors / drops (total)" className="py-1 tabular-nums">{i.rx_errors + i.tx_errors} / {i.rx_drops + i.tx_drops}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </Card>
          <Card title="Disk IO">
            <ChartGroup id={id} range={range} selectors={["disk.r:", "disk.w:"]} title={() => "Disk throughput"} max={4} />
            <ul className="mt-3 flex flex-wrap gap-3 text-sm">
              {(st?.disks ?? []).map((d) => (
                <li key={d.name}><span className="font-mono text-xs">{d.name}</span>: read {fmtByteRate(d.read_bps)}, write {fmtByteRate(d.write_bps)}, busy {fmtPct(d.busy_percent)}</li>
              ))}
            </ul>
          </Card>
          <Card title="Temperatures">
            <ChartGroup id={id} range={range} selectors={["temp:"]} title={(_, i) => `Sensors${i ? ` (${i + 1})` : ""}`} max={3} />
            {(st?.sensors ?? []).length === 0 && <p className="text-sm text-muted">No temperature sensors are exposed on this host (normal for VMs and containers).</p>}
            {(st?.sensors ?? []).length > 0 && <p className="mt-2 text-xs text-muted">Hottest now: {fmtTemp(Math.max(...st!.sensors.map((s) => s.celsius)))}</p>}
          </Card>
          {(st?.gpus ?? []).length > 0 && (
            <Card title="GPU">
              <ChartGroup id={id} range={range} selectors={["gpu.util:", "gpu.mem:", "gpu.temp:", "gpu.power:"]} title={(k) => (seriesUnit(k) === "pct" ? "GPU and VRAM use" : seriesUnit(k) === "temp" ? "GPU temperature" : "GPU power")} max={3} />
            </Card>
          )}
        </>
      )}
    </div>
  );
}
