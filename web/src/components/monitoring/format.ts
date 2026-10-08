/** Formatting for monitoring values. Unknown is never zero: every
 * formatter returns "—" (or a word) for null and undefined. */

export function fmtPct(v: number | null | undefined, digits = 0): string {
  return v == null ? "—" : `${v.toFixed(digits)}%`;
}

export function fmtTemp(v: number | null | undefined): string {
  return v == null ? "—" : `${Math.round(v)} °C`;
}

/** Bytes per second as bits for networks ("12.3 Mbit/s"). */
export function fmtBitRate(bps: number | null | undefined): string {
  if (bps == null) return "—";
  const bits = bps * 8;
  const u = ["bit/s", "kbit/s", "Mbit/s", "Gbit/s", "Tbit/s"];
  let i = 0;
  let v = bits;
  while (v >= 1000 && i < u.length - 1) {
    v /= 1000;
    i++;
  }
  return `${v >= 100 || i === 0 ? v.toFixed(0) : v.toFixed(1)} ${u[i]}`;
}

/** Bytes per second for disks ("4.1 MB/s"). */
export function fmtByteRate(bps: number | null | undefined): string {
  if (bps == null) return "—";
  const u = ["B/s", "KB/s", "MB/s", "GB/s"];
  let i = 0;
  let v = bps;
  while (v >= 1024 && i < u.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v >= 100 || i === 0 ? v.toFixed(0) : v.toFixed(1)} ${u[i]}`;
}

export function fmtHours(h: number | null | undefined): string {
  if (h == null) return "—";
  if (h < 48) return `${h} h`;
  const d = h / 24;
  return d < 730 ? `${Math.round(d)} days` : `${(d / 365).toFixed(1)} years`;
}

export function fmtSince(ms: number | null | undefined): string {
  if (!ms) return "—";
  const s = Math.round((Date.now() - ms) / 1000);
  if (s < 60) return "just now";
  if (s < 3600) return `${Math.floor(s / 60)} min ago`;
  if (s < 86400) return `${Math.floor(s / 3600)} h ago`;
  return `${Math.floor(s / 86400)} d ago`;
}

export function fmtTime(ms: number | null | undefined): string {
  if (!ms) return "—";
  return new Date(ms).toLocaleString();
}

/** A metric key's human label: "fs:/home" → "/home used". */
export function seriesLabel(key: string): string {
  const [m, label] = key.includes(":") ? [key.slice(0, key.indexOf(":")), key.slice(key.indexOf(":") + 1)] : [key, ""];
  const names: Record<string, string> = {
    cpu: "CPU", "cpu.iowait": "IO wait", mem: "Memory", swap: "Swap", load1: "Load (1 min)", load5: "Load (5 min)", load15: "Load (15 min)",
    "psi.cpu": "CPU pressure", "psi.mem": "Memory pressure", "psi.io": "IO pressure",
    fs: "used", inodes: "inodes used", "net.rx": "received", "net.tx": "sent", "net.err": "errors/s", "net.drop": "drops/s",
    "disk.r": "read", "disk.w": "written", "disk.busy": "busy", temp: "", "gpu.util": "GPU", "gpu.mem": "VRAM", "gpu.temp": "GPU temperature",
    "gpu.power": "GPU power",
  };
  const n = names[m] ?? m;
  if (!label) return n;
  if (m === "temp") return label;
  if (m.startsWith("gpu.")) return `${n} ${label}`;
  return `${label} ${n}`.trim();
}

/** How a series' values are shown. */
export function seriesUnit(key: string): "pct" | "bits" | "bytes" | "temp" | "watts" | "plain" {
  if (key.startsWith("net.rx:") || key.startsWith("net.tx:")) return "bits";
  if (key.startsWith("disk.r:") || key.startsWith("disk.w:")) return "bytes";
  if (key.startsWith("temp:") || key.startsWith("gpu.temp:")) return "temp";
  if (key.startsWith("gpu.power:")) return "watts";
  if (key.startsWith("load") || key.startsWith("net.err:") || key.startsWith("net.drop:")) return "plain";
  return "pct";
}

export function fmtValue(unit: ReturnType<typeof seriesUnit>, v: number | null | undefined): string {
  switch (unit) {
    case "pct":
      return fmtPct(v, v != null && v < 10 ? 1 : 0);
    case "bits":
      return fmtBitRate(v);
    case "bytes":
      return fmtByteRate(v);
    case "temp":
      return fmtTemp(v);
    case "watts":
      return v == null ? "—" : `${v.toFixed(0)} W`;
  }
  return v == null ? "—" : v.toFixed(2);
}
