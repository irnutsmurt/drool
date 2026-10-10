import { Link } from "react-router";
import { Badge } from "@/components/ui/Badge";
import { describeKey, shortSha, type DesiredDiff } from "./queries";

const statusLabels: Record<string, [string, "ok" | "warn" | "danger" | "neutral" | "accent"]> = {
  compliant: ["Compliant", "ok"],
  drift: ["Drift detected", "warn"],
  conflict: ["Conflict", "danger"],
  unknown: ["Unknown", "neutral"],
  reconciling: ["Restoring…", "accent"],
  failed: ["Restore failed", "danger"],
  none: ["Nothing managed", "neutral"],
};

/** A resource's or host's desired-state status. */
export function DesiredStatusBadge({ status }: { status: string }) {
  const [label, tone] = statusLabels[status] ?? [status, "neutral"];
  return <Badge tone={tone}>{label}</Badge>;
}

export const statusLabel = (s: string) => statusLabels[s]?.[0] ?? s;

const kindLabels: Record<string, string> = { service: "Service", package: "Package", file: "File" };
export const kindLabel = (k: string) => kindLabels[k] ?? k;

const fieldLabels: Record<string, string> = {
  state: "State", enabled: "At boot", installed: "Installed", exists: "Present", type: "Type", content: "Content",
  owner: "Owner", group: "Group", mode: "Permissions",
};

/** What differs, field by field. */
export function DiffTable({ diffs }: { diffs: DesiredDiff[] }) {
  if (diffs.length === 0) return <p className="text-sm text-muted">Nothing differs.</p>;
  return (
    <table className="w-full text-left text-sm" aria-label="What differs">
      <thead>
        <tr className="border-b border-line text-xs uppercase tracking-wide text-muted">
          <th className="py-1.5 pr-3 font-medium">What</th>
          <th className="py-1.5 pr-3 font-medium">Desired</th>
          <th className="py-1.5 font-medium">Current</th>
        </tr>
      </thead>
      <tbody>
        {diffs.map((d) => (
          <tr key={d.field} className="border-b border-line last:border-0">
            <td className="py-1.5 pr-3 font-medium">{fieldLabels[d.field] ?? d.field}</td>
            <td className="py-1.5 pr-3 font-mono text-xs">{d.field === "content" ? `sha256 ${shortSha(d.desired)}` : d.desired}</td>
            <td className="py-1.5 font-mono text-xs">{d.field === "content" ? `sha256 ${shortSha(d.current)}` : d.current}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

/** One line: "state: running → failed; at boot: …". */
export function diffLine(diffs: DesiredDiff[]): string {
  return diffs.map((d) => `${(fieldLabels[d.field] ?? d.field).toLowerCase()}: ${d.field === "content" ? shortSha(d.desired) : d.desired} → ${d.field === "content" ? shortSha(d.current) : d.current}`).join("; ");
}

export function ResourceName({ k }: { k: string }) {
  return <span className="font-mono text-xs">{describeKey(k)}</span>;
}

/** Big number tiles; the healthy one recedes. */
export function CountTile({ label, value, tone, to, quiet }: { label: string; value: number; tone: "ok" | "warn" | "danger" | "neutral"; to?: string; quiet?: boolean }) {
  const color = value === 0 || quiet ? "text-muted" : tone === "danger" ? "text-danger" : tone === "warn" ? "text-warn" : tone === "ok" ? "text-ok" : "text-fg";
  const body = (
    <>
      <div className={`text-2xl font-semibold ${color}`}>{value}</div>
      <div className="text-xs text-muted">{label}</div>
    </>
  );
  return to && value > 0 ? (
    <Link to={to} className="block rounded-ui border border-line bg-surface px-4 py-3 hover:bg-surface-2">{body}</Link>
  ) : (
    <div className="rounded-ui border border-line bg-surface px-4 py-3">{body}</div>
  );
}
