import type { ReactNode } from "react";
import { Link } from "react-router";
import { useHosts, useJobs, type Host } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { attentionReasons } from "@/components/hosts/health";
import { JobStatusBadge } from "@/components/jobs/badges";
import { primaryLink } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { EmptyState } from "@/components/ui/EmptyState";
import { PageHeader } from "@/components/ui/PageHeader";
import { cx } from "@/components/ui/cx";
import { plainLink } from "@/components/ui/links";
import { errorMessage, formatDateTime } from "@/lib/format";
import { summarizeCounts } from "./jobs/summary";
import { useAlerts, useAlertSummary } from "@/components/alerts/queries";
import { EventList } from "@/components/alerts/EventList";
import { AlertSeverity } from "@/components/alerts/ui";
import { useDesiredSummary } from "@/components/desired/queries";
import { useFwHosts } from "@/components/firewall/queries";
import { SecurityNote } from "@/components/layout/AppShell";
import { useSecurityItems } from "@/components/layout/security";

type Tone = "ok" | "warn" | "danger";

/** One area at a glance. Never a number before the data is there. */
function Tile({ label, value, detail, tone, to, loading, error }: {
  label: string; value?: ReactNode; detail?: ReactNode; tone?: Tone; to: string; loading?: boolean; error?: unknown;
}) {
  return (
    <Link to={to} className="block min-w-0 rounded-ui border border-line bg-surface p-3 hover:bg-surface-2 sm:p-4" data-testid={`tile-${label.toLowerCase().replace(/\W+/g, "-")}`}>
      <div className="text-xs font-medium uppercase tracking-wide text-muted">{label}</div>
      {error ? <div className="mt-1 text-sm text-danger">Couldn't load: {errorMessage(error)}</div>
        : loading ? <div className="mt-1 text-sm text-muted">Loading…</div> : (
          <>
            <div className={cx("mt-1 text-lg font-semibold sm:text-xl", tone === "danger" && "text-danger", tone === "warn" && "text-warn", tone === "ok" && "text-ok")}>{value}</div>
            {detail && <div className="mt-1 text-xs text-muted">{detail}</div>}
          </>
        )}
    </Link>
  );
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

const plural = (n: number, one: string, many = one + "s") => `${n} ${n === 1 ? one : many}`;

/**
 * The Overview: fleet triage. Are the machines okay, what needs
 * attention, what is running, is desired state drifting, is there a
 * firewall or security problem — each answer linking to where to act.
 */
export function DashboardPage() {
  const { can } = useAuth();
  const hostsQ = useHosts();
  const all = hostsQ.data?.items ?? [];
  const managed = all.filter((h) => !h.jump_only);
  const header = <PageHeader title="Overview" description="Fleet triage: what needs your attention right now." />;

  // Until the hosts arrive nothing is known: no counts, no "nothing needs
  // attention".
  if (!hostsQ.data) {
    return (
      <>
        {header}
        <p className="text-sm text-muted" role={hostsQ.isError ? "alert" : "status"}>
          {hostsQ.isError ? `Couldn't load the hosts: ${errorMessage(hostsQ.error)}` : "Loading…"}
        </p>
      </>
    );
  }

  if (managed.length === 0) {
    return (
      <>
        {header}
        <EmptyState title="Welcome to DROOL"
          body={<>DROOL manages Linux machines over SSH, or through a Shepherd (the DROOL agent) installed on them. Add your first host to see
            its health, updates and anything that needs attention here.</>}
          action={can("hosts.write") ? <Link className={primaryLink} to="/hosts/new">Add host</Link>
            : <span className="text-sm text-muted">Ask an administrator to add hosts.</span>} />
      </>
    );
  }

  return (
    <>
      {header}
      <StatusTiles managed={managed} />
      <div className="mt-4 grid gap-4 lg:grid-cols-2">
        <AttentionCard managed={managed} />
        {can("alerts.read") && <AlertsCard />}
        {can("jobs.read") && <RunsCard />}
        <UpdatesCard managed={managed} />
        <SecurityCard />
        {can("alerts.read") && <EventsCard />}
      </div>
    </>
  );
}

function StatusTiles({ managed }: { managed: Host[] }) {
  const { can } = useAuth();
  const alerts = useAlertSummary(can("alerts.read"));
  const jobs = useJobs();
  const desired = useDesiredSummary(can("desired.read"));
  const fw = useFwHosts(can("firewall.read"));
  const checked = managed.filter((h) => h.health && !h.health.stale);
  const online = checked.filter((h) => h.health!.reachable).length;
  const offline = checked.filter((h) => !h.health!.reachable).length;
  const stale = managed.length - checked.length;
  const a = alerts.data;
  const running = (jobs.data?.items ?? []).filter((j) => j.status === "pending" || j.status === "running");
  const paused = (jobs.data?.items ?? []).filter((j) => j.status === "paused");
  const d = desired.data;
  const fwHosts = fw.data?.items ?? [];
  const fwDrift = fwHosts.filter((h) => h.status === "drift").length;
  const fwBlocked = fwHosts.filter((h) => h.status === "conflicts").length;
  const fwPending = fwHosts.filter((h) => h.status === "pending_transaction" || h.status === "changes_pending" || h.status === "sets_pending").length;
  const fwEnforced = fwHosts.filter((h) => h.enforced).length;
  return (
    <div className="grid grid-cols-2 gap-2 sm:gap-3 xl:grid-cols-5">
      <Tile label="Hosts" to="/hosts" value={plural(managed.length, "host")}
        tone={offline ? "danger" : stale ? "warn" : "ok"}
        detail={`${online} online · ${offline} offline · ${stale} without recent status`} />
      {can("alerts.read") && (
        <Tile label="Alerts" to="/alerts" loading={!a} error={alerts.error}
          value={a ? (a.active + a.acknowledged === 0 ? "No open alerts" : `${a.active + a.acknowledged} open`) : undefined}
          tone={a ? (a.critical ? "danger" : a.warning ? "warn" : "ok") : undefined}
          detail={a && a.active + a.acknowledged > 0 ? `${a.critical} critical · ${a.warning} warning · ${a.acknowledged} acknowledged` : a?.suppressed ? `${a.suppressed} suppressed by maintenance` : undefined} />
      )}
      {can("jobs.read") && (
        <Tile label="Runs" to="/jobs" loading={!jobs.data} error={jobs.error}
          value={jobs.data ? (running.length ? `${running.length} running` : paused.length ? `${paused.length} paused` : "Nothing running") : undefined}
          tone={paused.length ? "warn" : undefined}
          detail={paused.length ? `${running.length ? `${paused.length} paused, ` : ""}waiting for a decision` : running.length ? "in progress now" : "no runs in progress"} />
      )}
      {can("desired.read") && (
        <Tile label="Desired State" to="/desired" loading={!d} error={desired.error}
          value={d ? (d.baselines === 0 ? "No baselines" : d.hosts.drift + d.resources.conflict + d.resources.failed > 0 ? `${plural(d.hosts.drift, "host")} drifted`
            : d.hosts.unknown > 0 ? `${plural(d.hosts.unknown, "host")} not checked` : "No drift") : undefined}
          tone={d && d.baselines > 0 ? (d.resources.conflict || d.resources.failed ? "danger" : d.hosts.drift ? "warn" : d.hosts.unknown ? undefined : "ok") : undefined}
          detail={d && d.baselines > 0 ? <>{d.resources.conflict > 0 && `${plural(d.resources.conflict, "conflict")} · `}{d.resources.failed > 0 && `${plural(d.resources.failed, "failed restore")} · `}{d.hosts.compliant} of {d.hosts.total} compliant{d.resources.unknown > 0 && ` · ${d.resources.unknown} unknown`}</> : d ? "nothing is managed yet" : undefined} />
      )}
      {can("firewall.read") && (
        <Tile label="Firewall" to="/firewall" loading={!fw.data} error={fw.error}
          value={fw.data ? (fwDrift + fwBlocked ? [fwDrift && `${fwDrift} drifted`, fwBlocked && `${fwBlocked} refused`].filter(Boolean).join(" · ")
            : fwPending ? `${plural(fwPending, "host")} not applied yet` : fwEnforced ? "In sync" : "Not enforced") : undefined}
          tone={fw.data ? (fwDrift || fwBlocked ? "danger" : fwPending ? "warn" : fwEnforced ? "ok" : undefined) : undefined}
          detail={fw.data ? `${plural(fwEnforced, "host")} enforced${fwPending ? ` · ${fwPending} with changes waiting` : ""}` : undefined} />
      )}
    </div>
  );
}

interface Item {
  key: string;
  tone: "danger" | "warn";
  body: ReactNode;
}

/** The inbox: what needs a person, across the fleet, most serious first. */
function AttentionCard({ managed }: { managed: Host[] }) {
  const { can } = useAuth();
  const alerts = useAlertSummary(can("alerts.read"));
  const jobs = useJobs();
  const desired = useDesiredSummary(can("desired.read"));
  const fw = useFwHosts(can("firewall.read"));
  const items: Item[] = [];

  const a = alerts.data;
  if (a && a.active_critical > 0) {
    items.push({ key: "critical", tone: "danger", body: <><Link className={plainLink} to="/alerts?severity=critical">{plural(a.active_critical, "critical alert")}</Link> not acknowledged</> });
  }
  for (const j of (jobs.data?.items ?? []).filter((x) => x.status === "paused")) {
    items.push({ key: "job" + j.id, tone: "warn", body: <>Run paused, waiting for a decision: <Link className={plainLink} to={`/jobs/${j.id}`}>{j.name}</Link></> });
  }
  const d = desired.data;
  if (d) {
    if (d.resources.conflict > 0) items.push({ key: "ds-conflict", tone: "danger", body: <>Desired State: <Link className={plainLink} to="/desired?status=conflict">{plural(d.resources.conflict, "conflict")}</Link> to resolve</> });
    if (d.resources.failed > 0) items.push({ key: "ds-failed", tone: "danger", body: <>Desired State: <Link className={plainLink} to="/desired?status=failed">{plural(d.resources.failed, "failed restore")}</Link></> });
    if (d.hosts.drift > 0) items.push({ key: "ds-drift", tone: "warn", body: <>Desired State: <Link className={plainLink} to="/desired?status=drift">{plural(d.hosts.drift, "host")} with drift</Link></> });
    for (const p of d.auto_paused) items.push({ key: "auto" + p.id, tone: "warn", body: <>Automatic restore paused for <Link className={plainLink} to={`/desired/baselines/${p.id}`}>{p.name}</Link>: {p.attention}</> });
  }
  for (const h of (fw.data?.items ?? [])) {
    if (h.status === "drift") items.push({ key: "fw" + h.host_id, tone: "danger", body: <>Firewall drift on <Link className={plainLink} to={`/hosts/${h.host_id}/firewall`}>{h.host_name}</Link>: DROOL&apos;s rules were changed outside DROOL</> });
    if (h.status === "conflicts") items.push({ key: "fwc" + h.host_id, tone: "danger", body: <>Firewall on <Link className={plainLink} to={`/hosts/${h.host_id}/firewall`}>{h.host_name}</Link>: applying is refused, a policy would cut DROOL&apos;s management path</> });
  }
  if (a?.destinations && a.destinations.failing > 0) {
    items.push({ key: "notify", tone: "warn", body: <><Link className={plainLink} to="/settings/notifications">{plural(a.destinations.failing, "notification destination")}</Link> failing: people may not be told about alerts</> });
  }
  const hostItems = managed.map((h) => ({ host: h, reasons: attentionReasons(h) })).filter((x) => x.reasons.length > 0);
  const severe = (r: string[]) => r.some((x) => /changed|denied|offline|full/.test(x));
  hostItems.sort((x, y) => Number(severe(y.reasons)) - Number(severe(x.reasons)));
  for (const { host, reasons } of hostItems.slice(0, 8)) {
    items.push({ key: "host" + host.id, tone: severe(reasons) ? "danger" : "warn",
      body: <><Link className={plainLink} to={`/hosts/${host.id}`}>{host.name}</Link>: {reasons.join(" · ")}</> });
  }
  // A source that couldn't be read is never "nothing to see" (DESIGN-STANDARD §8).
  const failed = [can("alerts.read") && alerts.isError && "alerts", jobs.isError && "runs",
    can("desired.read") && desired.isError && "Desired State", can("firewall.read") && fw.isError && "the firewall"].filter(Boolean) as string[];
  if (failed.length > 0) items.push({ key: "unchecked", tone: "warn", body: <>Couldn&apos;t check {failed.join(", ")}: this list may be incomplete.</> });
  items.sort((x, y) => (x.tone === y.tone ? 0 : x.tone === "danger" ? -1 : 1));
  const loading = (can("alerts.read") && alerts.isPending) || jobs.isPending || (can("desired.read") && desired.isPending) || (can("firewall.read") && fw.isPending);
  const more = hostItems.length - 8;
  return (
    <Card title={items.length ? `Needs attention (${items.length}${more > 0 ? "+" : ""})` : "Needs attention"}>
      {items.length === 0 ? (
        <p className="text-sm" role="status">{loading ? <span className="text-muted">Checking…</span> : <><span className="text-ok" aria-hidden="true">✓ </span>Nothing needs attention.</>}</p>
      ) : (
        <ul className="space-y-2 text-sm" data-testid="fleet-attention">
          {items.map((x) => (
            <li key={x.key} className="flex gap-2">
              <span className={cx("shrink-0 font-semibold", x.tone === "danger" ? "text-danger" : "text-warn")} aria-hidden="true">{x.tone === "danger" ? "✕" : "⚠"}</span>
              <span className="sr-only">{x.tone === "danger" ? "Problem:" : "Warning:"}</span>
              <span className="min-w-0">{x.body}</span>
            </li>
          ))}
        </ul>
      )}
      {more > 0 && <p className="mt-2 text-xs text-muted">…and {plural(more, "more host")}: see <Link className={plainLink} to="/hosts">Hosts</Link>.</p>}
      {items.length > 0 && loading && <p className="mt-2 text-xs text-muted">Still checking some areas…</p>}
    </Card>
  );
}

function AlertsCard() {
  const sum = useAlertSummary();
  const list = useAlerts({ view: "open" });
  const s = sum.data;
  const rows = (list.data?.pages[0]?.items ?? []).slice(0, 6);
  return (
    <Card title="Alerts" actions={<Link className="text-sm text-accent" to="/alerts">All alerts</Link>}>
      {!s ? <p className="text-sm text-muted">{sum.isError ? `Couldn't load alerts: ${errorMessage(sum.error)}` : "Loading…"}</p> : (
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
          <li key={a.id} className="flex items-start gap-2">
            <span className="shrink-0"><AlertSeverity severity={a.severity} /></span>
            <span className="min-w-0">
              <Link className="text-accent" to={`/alerts/${a.id}`}>{a.host_name}: {a.title}</Link>
              {a.acknowledgement && <span className="ml-2 text-xs text-muted">acknowledged</span>}
            </span>
          </li>
        ))}
      </ul>
    </Card>
  );
}

function RunsCard() {
  const jobsQ = useJobs();
  const jobs = jobsQ.data?.items ?? [];
  const running = jobs.filter((j) => ["pending", "running", "paused"].includes(j.status));
  const recent = jobs.filter((j) => !running.includes(j)).slice(0, Math.max(3, 6 - running.length));
  return (
    <Card title="Runs" actions={<Link className="text-sm text-accent" to="/jobs">All runs</Link>}>
      {!jobsQ.data ? (
        <p className="text-sm text-muted">{jobsQ.isError ? "Couldn't load runs." : "Loading…"}</p>
      ) : running.length === 0 && recent.length === 0 ? (
        <p className="text-sm text-muted">No runs yet.</p>
      ) : (
        <ul className="space-y-2 text-sm">
          {[...running, ...recent].map((j) => (
            <li key={j.id} className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
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
  );
}

/** Updates and reboots waiting across the fleet, with the way to act. */
function UpdatesCard({ managed }: { managed: Host[] }) {
  const { can } = useAuth();
  const updatable = managed.filter((h) => (h.health?.updates_available ?? 0) > 0);
  const updates = total(managed, (h) => h.health?.updates_available);
  const security = total(managed, (h) => h.health?.security_updates);
  const reboot = managed.filter((h) => h.health?.reboot_required);
  const failedUnits = total(managed, (h) => h.health?.failed_units);
  const failedHosts = managed.filter((h) => (h.health?.failed_units ?? 0) > 0).length;
  return (
    <Card title="Updates and reboots">
      <ul className="space-y-2 text-sm">
        <li>
          {updates == null ? <span className="text-muted">Pending updates aren&apos;t known yet (no package cache reported).</span> : updates === 0 ? "No pending updates." : (
            <>
              <b>{updates}</b> pending update{updates === 1 ? "" : "s"} on {plural(updatable.length, "host")}
              {security ? <span className="text-warn">, {security} security</span> : security === 0 ? ", none security" : ""}
              {can("jobs.execute") && <> · <Link className={plainLink} to={`/jobs/new?op=system_update&hosts=${updatable.map((h) => h.id).join(",")}`}>Install updates…</Link></>}
            </>
          )}
        </li>
        <li>
          {reboot.length === 0 ? "No host needs a reboot." : (
            <><span className="text-warn">{plural(reboot.length, "host")} need{reboot.length === 1 ? "s" : ""} a reboot</span>: {reboot.slice(0, 6).map((h, i) => <span key={h.id}>{i > 0 && ", "}<Link className={plainLink} to={`/hosts/${h.id}`}>{h.name}</Link></span>)}{reboot.length > 6 && " …"}</>
          )}
        </li>
        <li className="text-muted">{failedUnits ? <>{failedUnits} systemd unit{failedUnits === 1 ? " is" : "s are"} in the failed state on {plural(failedHosts, "host")}: only monitored services alert.</> : "No failed systemd units reported."}</li>
      </ul>
    </Card>
  );
}

/** Unresolved security posture, in full (the header shows it compactly). */
function SecurityCard() {
  const items = useSecurityItems();
  const { can } = useAuth();
  if (items.length === 0) return null;
  return (
    <Card title="Security">
      <div className="space-y-3" data-testid="security-card">
        {items.map((x) => <SecurityNote key={x.id} item={x} />)}
        {can("settings.read") && (
          <p className="text-sm"><Link className={plainLink} to="/settings/system#secure">Secure your installation: every check</Link></p>
        )}
      </div>
    </Card>
  );
}

function EventsCard() {
  return (
    <Card title="Recent events">
      <EventList limit={8} />
    </Card>
  );
}
