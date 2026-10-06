import type { Host } from "@/api/queries";
import { Badge } from "@/components/ui/Badge";
import { formatAgo } from "@/lib/format";

/** Reachability from the latest poll, flagged when stale. */
export function ReachabilityBadge({ host }: { host: Host }) {
  const h = host.health;
  if (!h) return <Badge>not checked yet</Badge>;
  if (h.stale) return <span title={`Last checked ${formatAgo(h.checked_at)}`}><Badge tone="warn">stale</Badge></span>;
  return h.reachable ? <Badge tone="ok">online</Badge> : <Badge tone="danger">offline</Badge>;
}

/** One-line OS description from facts. */
export function osLabel(host: Host): string {
  const f = host.facts;
  if (!f) return "—";
  return f.distro_name || [f.distro, f.distro_version].filter(Boolean).join(" ") || f.os_family;
}

/** Reasons a host needs attention, most important first. */
export function attentionReasons(host: Host): string[] {
  const out: string[] = [];
  const h = host.health;
  if (host.status === "hostkey_mismatch") out.push("host key changed");
  if (host.status === "auth_failed") out.push("access denied");
  if (host.status === "pending_hostkey") out.push("needs host key approval");
  if (h && !h.reachable && !h.stale) out.push("offline");
  if (h?.stale) out.push(`no data since ${formatAgo(h.checked_at)}`);
  if (h?.failed_units) out.push(`${h.failed_units} failed unit${h.failed_units === 1 ? "" : "s"}`);
  if (h?.reboot_required) out.push("reboot required");
  if (h?.security_updates) out.push(`${h.security_updates} security update${h.security_updates === 1 ? "" : "s"}`);
  if (h?.max_fs_used_pct != null && h.max_fs_used_pct >= 90) out.push(`${h.max_fs_mount} ${Math.round(h.max_fs_used_pct)}% full`);
  return out;
}
