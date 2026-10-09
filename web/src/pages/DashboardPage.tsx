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
import { errorMessage, formatDateTime } from "@/lib/format";
import { summarizeCounts } from "./jobs/summary";
import { useAlerts, useAlertSummary, useEvents } from "@/components/alerts/queries";
import { AlertSeverity } from "@/components/alerts/ui";
import { StatusIcon } from "@/components/monitoring/Status";
import { fmtSince } from "@/components/monitoring/format";
import { useDesiredSummary } from "@/components/desired/queries";

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

  // Until the hosts arrive nothing is known: no counts, no "nothing needs
  // attention".
  if (!hostsQ.data) {
    return (
      <>
        <PageHeader title="Dashboard" />
        <p className="text-sm text-muted">{hostsQ.isError ? `Couldn't load the hosts: ${errorMessage(hostsQ.error)}` : "Loading…"}</p>
      </>
    );
  }

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
  const updatable = managed.filter((h) => (h.health?.updates_available ?? 0) > 0);
  const withUpdates = updatable.length;
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
        <Stat label="Updates" value={updates ?? "—"} detail={updates ? `on ${withUpdates} host${withUpdates === 1 ? "" : "s"}` : "pending packages"}
          to={withUpdates && can("jobs.execute") ? `/jobs/new?op=system_update&hosts=${updatable.map((h) => h.id).join(",")}` : undefined} />
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
        {can("alerts.read") && <AlertsCard />}
        {can("desired.read") && <DesiredCard />}
        {can("alerts.read") && <EventsCard />}
        <Card title="Jobs">
          {!jobsQ.data ? (
            <p className="text-sm text-muted">{jobsQ.isError ? "Couldn't load jobs." : "Loading…"}</p>
          ) : running.length === 0 && recent.length === 0 ? (
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

function AlertsCard() {
  const sum = useAlertSummary();
  const list = useAlerts({ view: "open" });
  const s = sum.data;
  const rows = (list.data?.pages[0]?.items ?? []).slice(0, 6);
  return (
    <Card title="Alerts" actions={<Link className="text-sm text-accent" to="/alerts">All alerts</Link>}>
      {s && (
        <p className="mb-2 text-sm">
          {s.active + s.acknowledged === 0 ? "No open alerts." : (
            <><b>{s.critical}</b> critical · <b>{s.warning}</b> warning · {s.acknowledged} acknowledged{s.suppressed ? ` · ${s.suppressed} suppressed by maintenance` : ""}</>
          )}
          {s.destinations?.configured === 0 && <> · <Link className="text-accent underline" to="/settings/notifications">Set up notifications</Link></>}
          {(s.destinations?.failing ?? 0) > 0 && <span className="text-warn"> · notifications failing</span>}
        </p>
      )}
      <ul className="space-y-2 text-sm">
        {rows.map((a) => (
          <li key={a.id} className="flex flex-wrap items-center gap-2">
            <AlertSeverity severity={a.severity} />
            <Link className="text-accent" to={`/alerts/${a.id}`}>{a.host_name}: {a.title}</Link>
            {a.acknowledgement && <span className="text-xs text-muted">acknowledged</span>}
          </li>
        ))}
      </ul>
    </Card>
  );
}

/** Desired state at a glance: what differs and what needs a decision;
 * compliance recedes. */
function DesiredCard() {
  const sum = useDesiredSummary();
  const s = sum.data;
  if (!s || s.baselines === 0) return null;
  const problems = [
    s.hosts.drift > 0 && { text: `${s.hosts.drift} host${s.hosts.drift === 1 ? "" : "s"} with drift`, tone: "text-warn", to: "/desired?status=drift" },
    s.resources.conflict > 0 && { text: `${s.resources.conflict} conflict${s.resources.conflict === 1 ? "" : "s"}`, tone: "text-danger", to: "/desired?status=conflict" },
    s.resources.failed > 0 && { text: `${s.resources.failed} failed restore${s.resources.failed === 1 ? "" : "s"}`, tone: "text-danger", to: "/desired?status=failed" },
    s.auto_paused.length > 0 && { text: `automatic restore paused for ${s.auto_paused.length} baseline${s.auto_paused.length === 1 ? "" : "s"}`, tone: "text-warn", to: "/desired" },
  ].filter(Boolean) as { text: string; tone: string; to: string }[];
  return (
    <Card title="Desired state" actions={<Link className="text-sm text-accent" to="/desired">Overview</Link>}>
      {problems.length === 0 ? (
        <p className="text-sm text-muted">{s.hosts.compliant} of {s.hosts.total} managed hosts compliant{s.resources.unknown > 0 ? ` · ${s.resources.unknown} unknown` : ""}.</p>
      ) : (
        <ul className="space-y-1 text-sm">
          {problems.map((p) => <li key={p.text}><Link className={cx("font-medium underline", p.tone)} to={p.to}>{p.text}</Link></li>)}
          <li className="text-xs text-muted">{s.hosts.compliant} of {s.hosts.total} managed hosts compliant{s.resources.unknown > 0 ? ` · ${s.resources.unknown} resources unknown` : ""}</li>
        </ul>
      )}
      {s.firewall && s.firewall.drift > 0 && <p className="mt-2 text-xs text-muted">Firewall: <Link className="text-accent" to="/firewall">{s.firewall.drift} host{s.firewall.drift === 1 ? "" : "s"} drifted</Link> (managed by Firewall)</p>}
    </Card>
  );
}

const eventStatus: Record<string, "critical" | "warning" | "info"> = { critical: "critical", warning: "warning", info: "info" };

/** Where an event's object lives in the UI. */
function eventLink(e: { object_type?: string; object_id?: string }): string | undefined {
  switch (e.object_type) {
    case "alert":
      return e.object_id ? `/alerts/${e.object_id}` : undefined;
    case "job":
      return e.object_id ? `/jobs/${e.object_id}` : undefined;
    case "schedule":
      return e.object_id ? `/jobs/schedules/${e.object_id}` : "/jobs/schedules";
    case "maintenance":
      return "/alerts?view=maintenance";
    case "baseline":
      return e.object_id ? `/desired/baselines/${e.object_id}` : "/desired/baselines";
    case "desired_host":
      return e.object_id ? `/hosts/${e.object_id}/desired` : "/desired";
    case "desired":
      return "/desired";
  }
  return undefined;
}

function EventsCard() {
  const ev = useEvents(undefined, 8);
  const rows = ev.data?.events ?? [];
  return (
    <Card title="Recent events">
      {!ev.data ? <p className="text-sm text-muted">{ev.isError ? "Couldn't load events." : "Loading…"}</p>
        : rows.length === 0 ? <p className="text-sm text-muted">No events yet.</p> : (
        <ul className="space-y-2 text-sm">
          {rows.map((e) => (
            <li key={e.id} className="flex items-start gap-2">
              <StatusIcon status={e.kind === "alert.recovered" || e.kind === "notifications.recovered" ? "ok" : e.kind === "alert.cleared" ? "info" : eventStatus[e.severity] ?? "info"} />
              {eventLink(e) ? <Link className="min-w-0 text-accent" to={eventLink(e)!}>{e.title}</Link> : <span className="min-w-0">{e.title}</span>}
              <span className="ml-auto shrink-0 text-xs text-muted">{fmtSince(e.ts)}</span>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}
