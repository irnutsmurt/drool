import { Link } from "react-router";
import { Badge } from "@/components/ui/Badge";
import { StatusBadge, StatusIcon } from "@/components/monitoring/Status";
import { fmtTime } from "@/components/monitoring/format";
import type { Alert } from "./queries";

/** Alert severity: an icon and a word, never colour alone. */
export function AlertSeverity({ severity }: { severity: string }) {
  return <StatusBadge status={severity === "critical" ? "critical" : "warning"}>{severity === "critical" ? "Critical" : "Warning"}</StatusBadge>;
}

export const categoryLabel: Record<string, string> = {
  cpu: "CPU", memory: "Memory", disk: "Disk space", storage: "Storage health", services: "Services", network: "Interfaces",
  temperature: "Temperature", gpu: "GPU", stale: "Unreachable / stale", fidelity: "Monitoring fidelity",
};

export const checkLabel: Record<string, string> = {
  cpu: "Sustained CPU", memory: "Memory pressure", filesystem: "Filesystem space", inodes: "Inodes", filesystem_readonly: "Filesystem read-only",
  device_readonly: "Device read-only", service_failed: "Service failed", service_stopped: "Watched service stopped", interface_down: "Interface down",
  temperature: "Temperature", storage_health: "Storage health", media_errors: "Media errors", kernel_storage: "Kernel storage errors",
  probe: "Write/read probe", stale: "No monitoring data", fidelity: "Reduced fidelity", gpu_utilization: "High GPU utilization",
};

/** Where an alert's subject is looked at on its host: a failed service
 * among the host's services (found and highlighted, as from the search),
 * storage and hardware on their tabs, anything else the monitoring summary. */
export function alertMonitoringLink(a: Alert): { to: string; label: string } {
  const base = `/hosts/${a.host_id}/monitoring`;
  if (a.category === "services" && a.subject) return { to: `${base}?tab=services&unit=${encodeURIComponent(a.subject)}`, label: `View ${a.subject}` };
  const tab = ({ services: "services", storage: "storage", temperature: "hardware", gpu: "hardware" } as Record<string, string>)[a.category ?? ""];
  return { to: tab ? `${base}?tab=${tab}` : base, label: "View monitoring" };
}

/** Whether a resolved alert closed by configuration (or its subject
 * disappeared) rather than recovering. */
export const clearedBy = (a: Alert) => a.resolution === "disabled" || a.resolution === "reconfigured" || a.resolution === "gone";

export const resolutionWord: Record<string, string> = {
  recovered: "Recovered", disabled: "Cleared: check turned off", reconfigured: "Cleared: threshold changed", gone: "Closed: no longer reported",
};

/** "7m 42s", "2h 5m", "3d 4h". */
export function fmtDuration(ms: number | null | undefined): string {
  if (ms == null) return "—";
  const s = Math.max(0, Math.floor(ms / 1000));
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
  return `${Math.floor(s / 86400)}d ${Math.floor((s % 86400) / 3600)}h`;
}

export function AlertStateBadge({ a }: { a: Alert }) {
  if (a.state === "resolved" && clearedBy(a)) return <Badge tone="neutral" wrap>{resolutionWord[a.resolution!]} (not a recovery)</Badge>;
  if (a.state === "resolved") return <StatusBadge status="ok">Resolved</StatusBadge>;
  if (a.state === "acknowledged") return <Badge tone="neutral">✓ Acknowledged</Badge>;
  return <Badge tone="neutral">● Active</Badge>;
}

/** Whether people were told: delivered, pending, failing, or suppressed. */
export function NotificationBadge({ a }: { a: Alert }) {
  const n = a.notification;
  switch (n.status) {
    case "delivered":
      return <StatusBadge status="ok">Notified</StatusBadge>;
    case "pending":
      return <Badge tone="neutral">⧗ Sending</Badge>;
    case "failing":
      return <StatusBadge status="warning">Delivery retrying</StatusBadge>;
    case "partial":
      return <StatusBadge status="warning">Partly delivered</StatusBadge>;
    case "failed":
      return <StatusBadge status="critical">Delivery failed</StatusBadge>;
    case "suppressed":
      return <Badge tone="neutral">⏸ Suppressed</Badge>;
  }
  return <span className="text-xs text-muted">No destination</span>;
}

export function MaintenanceNote({ a }: { a: Alert }) {
  if (!a.suppressed) return a.ever_suppressed ? <span className="text-xs text-muted">Was suppressed</span> : null;
  return (
    <span className="inline-flex items-center gap-1 text-xs text-muted" title={a.suppressed_by?.reason}>
      <span aria-hidden="true">⏸</span> Suppressed by maintenance
    </span>
  );
}

/** The host's open alert counts, compact (fleet rows, host pages). */
export function AlertCounts({ critical, warning, hostId }: { critical: number; warning: number; hostId?: string }) {
  if (!critical && !warning) return <span className="text-xs text-muted">None</span>;
  // The link's name is what it shows ("1 critical 2 warning"), then what
  // they are: a screen reader hears the visible words first.
  const body = (
    <span className="inline-flex items-center gap-2 text-xs">
      {critical > 0 && <span className="inline-flex items-center gap-1 text-danger"><StatusIcon status="critical" />{critical} critical</span>}
      {warning > 0 && <span className="inline-flex items-center gap-1 text-warn"><StatusIcon status="warning" />{warning} warning</span>}
      <span className="sr-only">open alerts</span>
    </span>
  );
  return hostId ? <Link to={`/alerts?view=active&host_id=${hostId}`} className="hover:underline">{body}</Link> : body;
}

export function untilText(endsAt: number | null | undefined): string {
  return endsAt ? `until ${fmtTime(endsAt)}` : "until ended manually";
}
