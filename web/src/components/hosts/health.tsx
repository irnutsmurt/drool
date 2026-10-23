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

/** What stands between DROOL and a host, from its last status check (the
 * server's verdicts): the host key, sign-in, reachability, and how old the
 * status is. Health (disk space, services, …) is monitoring's verdict and
 * shows as its open alerts; updates and reboots have their own line. */
export function connectionReasons(host: Host): string[] {
  const out: string[] = [];
  const h = host.health;
  if (host.status === "hostkey_mismatch") out.push("host key changed");
  if (host.status === "auth_failed") out.push("access denied");
  if (host.status === "pending_hostkey") out.push("needs host key approval");
  if (h && !h.reachable && !h.stale) out.push("offline");
  if (h?.stale) out.push(`no data since ${formatAgo(h.checked_at)}`);
  return out;
}

/** A host's pending updates in a list. From a host that isn't reporting
 * now (offline, or its status is old) the number is the last known one,
 * and says so; never a bare zero for what isn't known. */
export function UpdatesCell({ host }: { host: Host }) {
  const h = host.health;
  if (!h) return <span className="text-muted">not checked yet</span>;
  if (h.updates_available == null) return <span className="text-muted">unknown</span>;
  const old = h.stale || !h.reachable;
  const sec = h.security_updates;
  return (
    <span className={old ? "text-muted" : undefined} title={old ? `Checked ${formatAgo(h.updates_checked_at ?? h.checked_at)}` : undefined}>
      {h.updates_available}{sec ? <span className={old ? undefined : "text-warn"}> ({sec} security)</span> : null}{old && " · last known"}
    </span>
  );
}

/** Whether a host needs a reboot, in a list: "not required" isn't
 * unknown, and an old answer says it is old. */
export function RebootCell({ host }: { host: Host }) {
  const h = host.health;
  if (!h || h.reboot_required == null) return <span className="text-muted">unknown</span>;
  const old = h.stale || !h.reachable;
  if (!h.reboot_required) return <span className="text-muted">no{old && " · last known"}</span>;
  return old ? <span className="text-muted">required · last known</span> : <Badge tone="warn">required</Badge>;
}
