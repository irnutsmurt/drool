import { Link } from "react-router";
import { Card } from "@/components/ui/Card";
import { Alert } from "@/components/ui/Alert";
import { errorMessage } from "@/lib/format";
import { useHostMonitoring, type MonHostOverview } from "./queries";
import { StatusIcon, statusOf } from "./Status";
import { fmtSince } from "./format";

const tileTab: Record<string, string> = {
  cpu: "metrics", memory: "metrics", storage: "metrics", temperature: "hardware", network: "metrics",
  services: "services", devices: "storage", issues: "overview",
};

/** How the host is observed, in plain words. SSH is never "continuous". */
export function fidelityText(o: MonHostOverview): string {
  const f = o.state?.fidelity;
  if (!f) return "";
  if (f.mode === "continuous") {
    return `Continuous: the agent samples every ${f.sample_s} s${f.reduced ? " (reduced to save resources)" : ""}`;
  }
  return `Polled over SSH every ${Math.round(f.sample_s / 60)} min (not continuous)`;
}

export function MonitoringTiles({ hostId, o }: { hostId: string; o: MonHostOverview }) {
  return (
    <ul className="grid grid-cols-2 gap-2 sm:grid-cols-4">
      {o.tiles.map((t) => (
        <li key={t.key}>
          <Link to={`/hosts/${hostId}/monitoring?tab=${tileTab[t.key] ?? "overview"}`}
            className="flex h-full flex-col gap-1 rounded-ui border border-line p-3 hover:border-accent focus-visible:outline-2 focus-visible:outline-accent"
            aria-label={`${t.label}: ${t.text}${t.status !== "ok" ? ` (${t.status})` : ""}${t.detail ? `. ${t.detail}` : ""}. Open details`}>
            <span className="flex items-center justify-between text-xs text-muted">
              {t.label}
              <StatusIcon status={statusOf(t.status)} />
            </span>
            <span className="text-lg font-semibold tabular-nums">{t.text}</span>
            {t.detail && <span className="truncate text-xs text-muted" title={t.detail}>{t.detail}</span>}
          </Link>
        </li>
      ))}
    </ul>
  );
}

/** The host page's monitoring summary: the headline tiles, each a link
 * to details and history. */
export function HostMonitoringCard({ hostId }: { hostId: string }) {
  const q = useHostMonitoring(hostId);
  const o = q.data;
  return (
    <Card title="Monitoring" actions={<Link className="text-sm text-accent" to={`/hosts/${hostId}/monitoring`}>Details and history</Link>}>
      {q.error ? (
        <Alert>{errorMessage(q.error)}</Alert>
      ) : !o ? (
        <p className="text-sm text-muted">Loading…</p>
      ) : !o.has_data ? (
        <p className="text-sm text-muted">
          No monitoring data yet. Agent hosts report within a minute of connecting; SSH hosts on their next poll (every few minutes).
        </p>
      ) : (
        <div className="space-y-3">
          {o.stale && (
            <Alert tone="warn">No monitoring data since {fmtSince(o.updated_at)}: the values below are from before. The host may be unreachable.</Alert>
          )}
          <MonitoringTiles hostId={hostId} o={o} />
          <p className="text-xs text-muted">{fidelityText(o)} · updated {fmtSince(o.updated_at)}</p>
        </div>
      )}
    </Card>
  );
}
