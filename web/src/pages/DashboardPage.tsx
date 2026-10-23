import { useState, type ReactNode } from "react";
import { Link } from "react-router";
import { useHosts, useJobs, type Host } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { connectionReasons } from "@/components/hosts/health";
import { JobStatusBadge } from "@/components/jobs/badges";
import { primaryLink } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { EmptyState } from "@/components/ui/EmptyState";
import { PageHeader } from "@/components/ui/PageHeader";
import { cx } from "@/components/ui/cx";
import { plainLink, touchLink } from "@/components/ui/links";
import { errorMessage, formatDateTime } from "@/lib/format";
import { summarizeCounts } from "./jobs/summary";
import { useAlerts, useAlertSummary } from "@/components/alerts/queries";
import { AlertLine } from "@/components/alerts/AlertLine";
import { EventList } from "@/components/alerts/EventList";
import { useDesiredSummary } from "@/components/desired/queries";
import { useFwHosts } from "@/components/firewall/queries";
import { InstallUpdatesDialog } from "@/components/jobs/InstallUpdatesDialog";
import { TestConnectionDialog } from "@/components/hosts/TestConnectionDialog";

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
      {/* Most urgent first: what needs a person, then what can be done
          about updates, then what's running or just failed, then history.
          Security posture is in the header on every page. */}
      <div className="mt-4 grid gap-4 lg:grid-cols-2">
        <AttentionCard managed={managed} />
        <UpdatesCard managed={managed} />
        {can("jobs.read") && <RunsCard />}
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
      <Tile label="Hosts" to="/hosts"
        value={offline ? `${offline} offline` : stale ? `${stale} not reporting` : `All ${managed.length} online`}
        tone={offline ? "danger" : stale ? "warn" : "ok"}
        detail={`${plural(managed.length, "host")}: ${online} online · ${offline} offline · ${stale} without recent status`} />
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
          detail={d && d.baselines > 0 ? <>{d.resources.conflict > 0 && `${plural(d.resources.conflict, "conflict")} · `}{d.resources.failed > 0 && `${plural(d.resources.failed, "failed restore")} · `}{d.hosts.compliant} of {plural(d.hosts.total, "host")} compliant{d.resources.unknown > 0 && ` · ${plural(d.resources.unknown, "resource")} unknown`}</> : d ? "nothing is managed yet" : undefined} />
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

const shownAlerts = 6;

/** The inbox: what is failing or waiting for a decision, across the fleet,
 * most serious first, each saying what is wrong and offering the next step.
 * Built from the server's verdicts: hosts DROOL can't reach (a host's "no
 * monitoring data" alert folded into that row), open alerts in their own
 * words (with the service fix), paused runs, Desired State, the firewall and
 * failing notifications. Pending updates and reboots are routine work: the
 * Updates card owns them. Alerts under maintenance or acknowledged are
 * counted below the list, not in it. */
function AttentionCard({ managed }: { managed: Host[] }) {
  const { can } = useAuth();
  const alerts = useAlertSummary(can("alerts.read"));
  const open = useAlerts({ view: "active", suppressed: false }, can("alerts.read"));
  const jobs = useJobs();
  const desired = useDesiredSummary(can("desired.read"));
  const fw = useFwHosts(can("firewall.read"));
  const [testing, setTesting] = useState<Host | null>(null);
  const items: Item[] = [];

  // Hosts DROOL can't reach or manage: one row each, with the test.
  const conn = managed.map((h) => ({ host: h, reasons: connectionReasons(h) })).filter((x) => x.reasons.length > 0);
  const connHosts = new Set(conn.map((x) => x.host.id));
  for (const { host, reasons } of conn) {
    const sshHost = host.management?.active !== "agent";
    items.push({ key: "conn" + host.id, tone: reasons.some((x) => /changed|denied|offline/.test(x)) ? "danger" : "warn",
      body: (
        <>
          <Link className={plainLink} to={`/hosts/${host.id}`}>{host.name}</Link>: {reasons.join(" · ")}
          {can("hosts.write") && sshHost && !/needs host key/.test(reasons.join()) && (
            <> · <button type="button" className={cx(plainLink, touchLink)} onClick={() => setTesting(host)} aria-label={`Test connection to ${host.name}`}>Test connection…</button></>
          )}
        </>
      ) });
  }
  // Open alerts nobody has taken on yet, in their own words. "No
  // monitoring data" says the same as a connection row above.
  const alertRows = (open.data?.pages[0]?.items ?? []).filter((x) => !(x.check === "stale" && connHosts.has(x.host_id)));
  for (const x of alertRows.slice(0, shownAlerts)) {
    items.push({ key: "alert" + x.id, tone: x.severity === "critical" ? "danger" : "warn", body: <AlertLine a={x} withHost /> });
  }
  const moreAlerts = alertRows.length - shownAlerts + (open.data?.pages[0]?.next_cursor ? 1 : 0);
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
  const a = alerts.data;
  if (a?.destinations && a.destinations.failing > 0) {
    items.push({ key: "notify", tone: "warn", body: <><Link className={plainLink} to="/settings/notifications">{plural(a.destinations.failing, "notification destination")}</Link> failing: people may not be told about alerts</> });
  }
  // A source that couldn't be read is never "nothing to see" (DESIGN-STANDARD §8).
  const failed = [can("alerts.read") && (alerts.isError || open.isError) && "alerts", jobs.isError && "runs",
    can("desired.read") && desired.isError && "Desired State", can("firewall.read") && fw.isError && "the firewall"].filter(Boolean) as string[];
  if (failed.length > 0) items.push({ key: "unchecked", tone: "warn", body: <>Couldn&apos;t check {failed.join(", ")}: this list may be incomplete.</> });
  items.sort((x, y) => (x.tone === y.tone ? 0 : x.tone === "danger" ? -1 : 1));
  const loading = (can("alerts.read") && (alerts.isPending || open.isPending)) || jobs.isPending || (can("desired.read") && desired.isPending) || (can("firewall.read") && fw.isPending);
  const acknowledged = a?.acknowledged ?? 0;
  return (
    <Card title={items.length ? `Needs attention (${items.length}${moreAlerts > 0 ? "+" : ""})` : "Needs attention"}
      actions={can("alerts.read") && <Link className="text-sm text-accent" to="/alerts">All alerts</Link>}>
      {items.length === 0 ? (
        <p className="text-sm" role="status">{loading ? <span className="text-muted">Checking…</span> : <><span className="text-ok" aria-hidden="true">✓ </span>Nothing is failing or waiting for a decision.</>}</p>
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
      {moreAlerts > 0 && <p className="mt-2 text-xs text-muted">…and more open alerts: see <Link className={plainLink} to="/alerts">Alerts</Link>.</p>}
      {(acknowledged > 0 || (a?.suppressed ?? 0) > 0) && (
        <p className="mt-2 text-xs text-muted">
          {acknowledged > 0 && <>{plural(acknowledged, "acknowledged alert")}: someone is on it. </>}
          {(a?.suppressed ?? 0) > 0 && <>{plural(a!.suppressed, "alert")} suppressed by maintenance.</>}
        </p>
      )}
      {a?.destinations?.configured === 0 && (
        <p className="mt-2 text-xs text-muted">Alerts reach nobody yet: <Link className={plainLink} to="/settings/notifications">set up notifications</Link>.</p>
      )}
      {items.length > 0 && loading && <p className="mt-2 text-xs text-muted">Still checking some areas…</p>}
      {testing && <TestConnectionDialog host={testing} onClose={() => setTesting(null)} />}
    </Card>
  );
}

/** Runs that matter now: in progress or paused, and what failed in the
 * last day. Finished history is under Runs. */
function RunsCard() {
  const jobsQ = useJobs();
  const jobs = jobsQ.data?.items ?? [];
  const running = jobs.filter((j) => ["pending", "running", "paused"].includes(j.status));
  const dayAgo = Date.now() - 864e5;
  const recent = jobs.filter((j) => !running.includes(j) && ["failed", "partial", "interrupted", "timed_out"].includes(j.status) && Date.parse(j.created_at) > dayAgo).slice(0, 6);
  return (
    <Card title="Runs" actions={<Link className="text-sm text-accent" to="/jobs">All runs</Link>}>
      {!jobsQ.data ? (
        <p className="text-sm text-muted">{jobsQ.isError ? "Couldn't load runs." : "Loading…"}</p>
      ) : running.length === 0 && recent.length === 0 ? (
        <p className="text-sm text-muted">Nothing running, and nothing failed in the last day.</p>
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
  const [installing, setInstalling] = useState(false);
  // Counts only from hosts that report now: an offline or stale host's
  // numbers are from before (DESIGN-STANDARD §8), and "not known there".
  const reporting = managed.filter((h) => h.health && !h.health.stale && h.health.reachable);
  const updatable = reporting.filter((h) => (h.health?.updates_available ?? 0) > 0);
  const updates = total(reporting, (h) => h.health?.updates_available);
  const security = total(reporting, (h) => h.health?.security_updates);
  const reboot = reporting.filter((h) => h.health?.reboot_required);
  const failedUnits = total(reporting, (h) => h.health?.failed_units);
  const failedHosts = reporting.filter((h) => (h.health?.failed_units ?? 0) > 0).length;
  const silent = managed.length - reporting.length;
  const unknownThere = silent > 0 ? ` on the hosts that report (${plural(silent, "host")} ${silent === 1 ? "hasn't" : "haven't"} recently: not known there)` : "";
  return (
    <Card title="Updates and reboots">
      <ul className="space-y-2 text-sm">
        <li>
          {updates == null ? <span className="text-muted">Pending updates aren&apos;t known yet (no package cache reported).</span> : updates === 0 ? `No pending updates${unknownThere}.` : (
            <>
              <b>{updates}</b> pending update{updates === 1 ? "" : "s"} on {plural(updatable.length, "host")}
              {security ? <span className="text-warn">, {security} security</span> : security === 0 ? ", none security" : ""}
              {can("jobs.execute") && <> · <button type="button" className={cx(plainLink, touchLink)} onClick={() => setInstalling(true)}>Install updates…</button></>}
              {installing && <InstallUpdatesDialog targets={{ hosts: updatable.map((h) => h.id) }}
                label={updatable.length === 1 ? updatable[0]!.name : plural(updatable.length, "host")} onClose={() => setInstalling(false)} />}
            </>
          )}
        </li>
        <li>
          {reboot.length === 0 ? `No host needs a reboot${unknownThere}.` : (
            <><span className="text-warn">{plural(reboot.length, "host")} need{reboot.length === 1 ? "s" : ""} a reboot</span>: {reboot.slice(0, 6).map((h, i) => <span key={h.id}>{i > 0 && ", "}<Link className={plainLink} to={`/hosts/${h.id}`}>{h.name}</Link></span>)}{reboot.length > 6 && " …"}
              {can("jobs.execute") && <> · <Link className={plainLink} to={`/jobs/new?hosts=${reboot.map((h) => h.id).join(",")}&op=reboot`}>Reboot…</Link></>}</>
          )}
        </li>
        <li className="text-muted">{failedUnits ? <>{failedUnits} systemd unit{failedUnits === 1 ? " is" : "s are"} in the failed state on {plural(failedHosts, "host")}: only monitored services alert.</> : "No failed systemd units reported."}</li>
      </ul>
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
