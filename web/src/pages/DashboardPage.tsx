import type { ReactNode } from "react";
import { Link } from "react-router";
import { useHosts, useJobs, type Host } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { attentionReasons } from "@/components/hosts/health";
import { JobStatusBadge } from "@/components/jobs/badges";
import { Card } from "@/components/ui/Card";
import { EmptyState } from "@/components/ui/EmptyState";
import { PageHeader } from "@/components/ui/PageHeader";
import { cx } from "@/components/ui/cx";
import { formatDateTime } from "@/lib/format";
import { summarizeCounts } from "./jobs/summary";

function Stat({ label, value, detail, tone, to }: {
  label: string;
  value: ReactNode;
  detail?: ReactNode;
  tone?: "ok" | "warn" | "danger";
  to?: string;
}) {
  const body = (
    <div className="rounded-ui border border-line bg-surface p-4">
      <div className="text-xs font-medium uppercase tracking-wide text-muted">{label}</div>
      <div className={cx("mt-1 text-2xl font-semibold", tone === "danger" && "text-danger", tone === "warn" && "text-warn", tone === "ok" && "text-ok")}>
        {value}
      </div>
      {detail && <div className="mt-1 text-xs text-muted">{detail}</div>}
    </div>
  );
  return to ? <Link to={to} className="block hover:opacity-90">{body}</Link> : body;
}

/** Sum a nullable per-host number; null when no host reported it. */
function total(hosts: Host[], pick: (h: Host) => number | null | undefined): number | null {
  let sum: number | null = null;
  for (const h of hosts) {
    const v = pick(h);
    if (v != null) sum = (sum ?? 0) + v;
  }
  return sum;
}

export function DashboardPage() {
  const { can } = useAuth();
  const hostsQ = useHosts();
  const jobsQ = useJobs();
  const all = hostsQ.data?.items ?? [];
  const managed = all.filter((h) => !h.jump_only);

  if (hostsQ.data && managed.length === 0) {
    return (
      <>
        <PageHeader title="Dashboard" />
        <EmptyState title="Welcome to DROOL" body="Add your first Linux host to see its status here."
          action={can("hosts.write") && <Link className="text-accent" to="/hosts/new">Add a host</Link>} />
      </>
    );
  }

  const checked = managed.filter((h) => h.health && !h.health.stale);
  const online = checked.filter((h) => h.health!.reachable).length;
  const offline = checked.filter((h) => !h.health!.reachable).length;
  const stale = managed.filter((h) => !h.health || h.health.stale).length;
  const withUpdates = managed.filter((h) => (h.health?.updates_available ?? 0) > 0).length;
  const updates = total(managed, (h) => h.health?.updates_available);
  const security = total(managed, (h) => h.health?.security_updates);
  const reboot = managed.filter((h) => h.health?.reboot_required).length;
  const failedUnits = total(managed, (h) => h.health?.failed_units);
  const failedHosts = managed.filter((h) => (h.health?.failed_units ?? 0) > 0).length;
  const attention = managed
    .map((h) => ({ host: h, reasons: attentionReasons(h) }))
    .filter((a) => a.reasons.length > 0)
    .slice(0, 8);
  const jobs = jobsQ.data?.items ?? [];
  const running = jobs.filter((j) => ["pending", "running", "paused"].includes(j.status));
  const recent = jobs.filter((j) => !running.includes(j)).slice(0, 6);

  return (
    <>
      <PageHeader title="Dashboard" />
      <div className="grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-6">
        <Stat label="Hosts" value={managed.length} detail={`${online} online · ${offline} offline`} to="/hosts" />
        <Stat label="Offline" value={offline} tone={offline ? "danger" : undefined} detail="last check failed" />
        <Stat label="Stale" value={stale} tone={stale ? "warn" : undefined} detail="no recent status" />
        <Stat label="Updates" value={updates ?? "—"} detail={updates ? `on ${withUpdates} host${withUpdates === 1 ? "" : "s"}` : "pending packages"} />
        <Stat label="Security updates" value={security ?? "—"} tone={security ? "warn" : undefined}
          detail={security == null ? "not detectable yet" : "pending"} />
        <Stat label="Reboot required" value={reboot} tone={reboot ? "warn" : undefined}
          detail={failedUnits ? `${failedUnits} failed units on ${failedHosts} host${failedHosts === 1 ? "" : "s"}` : "no failed units"} />
      </div>

      <div className="mt-4 grid gap-4 lg:grid-cols-2">
        <Card title="Needs attention">
          {attention.length === 0 ? (
            <p className="text-sm text-muted">Nothing needs attention.</p>
          ) : (
            <ul className="space-y-2 text-sm">
              {attention.map(({ host, reasons }) => (
                <li key={host.id} className="flex flex-wrap items-baseline gap-x-3">
                  <Link className="font-medium text-accent" to={`/hosts/${host.id}`}>{host.name}</Link>
                  <span className="text-muted">{reasons.join(" · ")}</span>
                </li>
              ))}
            </ul>
          )}
        </Card>
        <Card title="Jobs">
          {running.length === 0 && recent.length === 0 ? (
            <p className="text-sm text-muted">No jobs yet.</p>
          ) : (
            <ul className="space-y-2 text-sm">
              {[...running, ...recent].map((j) => (
                <li key={j.id} className="flex items-center gap-2">
                  <JobStatusBadge job={j} />
                  <Link className="min-w-0 truncate text-accent" to={`/jobs/${j.id}`}>{j.name}</Link>
                  <span className="ml-auto shrink-0 text-xs text-muted">
                    {running.includes(j) ? summarizeCounts(j) : formatDateTime(j.created_at)}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </Card>
      </div>
    </>
  );
}
