import { useState, type ReactNode } from "react";
import { Link } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { qk, useHosts, useJobs, type Host } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { JobStatusBadge } from "@/components/jobs/badges";
import { HostAttention } from "@/components/hosts/HostAttention";
import { HostKeyPanel } from "@/components/hosts/HostKeyPanel";
import { ProbeSteps } from "@/components/hosts/ProbeSteps";
import { SystemCard } from "@/components/hosts/InventoryCards";
import { statusHelp } from "@/components/hosts/statusHelp";
import { useHostWorkspace, useLegacyHostAnchors } from "@/components/hosts/workspace/HostLayout";
import { hostPath } from "@/components/hosts/workspace/sections";
import { describeKey, useDesiredHost } from "@/components/desired/queries";
import { DesiredStatusBadge, statusLabel } from "@/components/desired/ui";
import { statusLabels as fwStatusLabels, modeInfo as fwModes } from "@/components/firewall/parts";
import { useFwHost } from "@/components/firewall/queries";
import { useHostNetwork } from "@/components/network/queries";
import { CollectionBadge } from "@/components/network/labels";
import { HostMonitoringSummary } from "@/components/monitoring/HostMonitoringSummary";
import { HostMaintenanceBanner } from "@/components/alerts/HostAlerts";
import { EventList } from "@/components/alerts/EventList";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Card } from "@/components/ui/Card";
import { LoadError, Loading } from "@/components/ui/States";
import { cx } from "@/components/ui/cx";
import { plainLink, touchLink } from "@/components/ui/links";
import { errorMessage, formatAgo, formatDateTime, formatUptime } from "@/lib/format";
import { InstallUpdatesDialog } from "@/components/jobs/InstallUpdatesDialog";

/** "View monitoring" and friends: the way from a summary to the section
 * that has the details. */
function ViewLink({ to, children, label }: { to: string; children: ReactNode; label?: string }) {
  return <Link className="whitespace-nowrap text-sm text-accent" to={to} aria-label={label}>{children} <span aria-hidden="true">→</span></Link>;
}

/**
 * A host at a glance — an operational summary, not every page squeezed
 * into one: first what needs a person, then how the host is doing now
 * (monitoring, alerts), then one line per area of its workspace, then
 * what happened lately. Each summary opens its section. The header above
 * already says who the host is and how DROOL reaches it.
 */
export function HostOverviewPage() {
  const { host: h } = useHostWorkspace();
  const qc = useQueryClient();
  const { can } = useAuth();
  useLegacyHostAnchors(h.id);
  const sshHost = h.management.ssh_configured;
  const needsKey = sshHost && (h.status === "pending_hostkey" || h.status === "hostkey_mismatch");
  const managed = !h.jump_only;
  const help = statusHelp(h);
  // Approving the host key runs the first connection test; its steps show here.
  const test = useMutation({
    mutationFn: () => unwrap(api.POST("/hosts/{id}/probe", { params: { path: { id: h.id } } })),
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: qk.host(h.id) });
      void qc.invalidateQueries({ queryKey: qk.hosts });
      void qc.invalidateQueries({ queryKey: qk.hostKeys(h.id) });
    },
  });

  return (
    <div className="space-y-4" data-testid="host-overview">
      {can("alerts.read") && managed && <HostMaintenanceBanner hostId={h.id} />}
      {needsKey && help && <Alert tone={help.tone}>{help.text}</Alert>}
      {needsKey && (
        <Card title={h.status === "hostkey_mismatch" ? "The host key changed" : "Approve the host key"}>
          <HostKeyPanel hostId={h.id} onApproved={() => test.mutate()} />
        </Card>
      )}
      {(test.data || test.error || test.isPending) && (
        <Card title="Connection test">
          {test.isPending ? <Loading what="the connection test" /> : test.error ? <Alert>{errorMessage(test.error)}</Alert> : test.data && <ProbeSteps result={test.data} />}
        </Card>
      )}
      {managed ? <HostAttention h={h} hideStatus={!!help} /> : <JumpHostNote h={h} />}

      {/* Its open alerts are under Needs attention, each once. */}
      {managed && (
        <Card title="Health now"
          actions={can("monitoring.read") && (
            <span className="flex items-center gap-4">
              <ViewLink to={hostPath(h.id, "monitoring", "tab=services")} label="Services of this host">Services</ViewLink>
              <ViewLink to={hostPath(h.id, "monitoring")}>Monitoring</ViewLink>
            </span>
          )}>
          <div className="space-y-3">
            {can("monitoring.read") && <HostMonitoringSummary hostId={h.id} />}
            <StatusLine h={h} />
          </div>
        </Card>
      )}

      {managed && <AreaTiles h={h} />}

      {managed && (
        <div className="grid gap-4 xl:grid-cols-3">
          <RecentRuns h={h} />
          {can("alerts.read") && (
            <Card title="Recent events" actions={<ViewLink to={`/alerts?view=resolved&host_id=${h.id}`}>Alert history</ViewLink>}>
              <EventList hostId={h.id} limit={6} />
            </Card>
          )}
          <SystemCard host={h} />
        </div>
      )}
    </div>
  );
}

/** Updates, reboot and the last status check in one line: the things a
 * person acts on (Install updates, Reboot) without opening anything. */
function StatusLine({ h }: { h: Host }) {
  const { can } = useAuth();
  const [installing, setInstalling] = useState(false);
  const s = h.health;
  if (!s) return <p className="text-sm text-muted">Status not checked yet: it is collected automatically every few minutes.</p>;
  return (
    <dl className="grid gap-x-6 gap-y-1 text-sm sm:grid-cols-2" data-testid="host-status-line">
      <div className="flex flex-wrap gap-x-2">
        <dt className="text-muted">Updates</dt>
        <dd>{s.updates_available == null ? <span className="text-muted">unknown (package cache not available yet)</span> : (
          <>{s.updates_available} pending{s.security_updates ? <span className="text-warn">, {s.security_updates} security</span> : ""}
            <span className="text-muted"> · checked {formatAgo(s.updates_checked_at)}</span>
            {s.updates_available > 0 && can("jobs.execute") && !h.jump_only && (
              <> · <button type="button" className={cx(plainLink, touchLink)} onClick={() => setInstalling(true)} data-testid="overview-install-updates">Install updates…</button></>
            )}
            {installing && <InstallUpdatesDialog targets={{ hosts: [h.id] }} label={h.name} onClose={() => setInstalling(false)} />}</>
        )}</dd>
      </div>
      <div className="flex flex-wrap gap-x-2">
        <dt className="text-muted">Reboot</dt>
        <dd>{s.reboot_required == null ? <span className="text-muted">unknown</span>
          : s.reboot_required ? <><span className="text-warn">required{s.reboot_reason && ` (${s.reboot_reason})`}</span>
            {can("jobs.execute") && !h.jump_only && <> · <Link className={plainLink} to={`/jobs/new?hosts=${h.id}&op=reboot`}>Reboot…</Link></>}</> : "not required"}</dd>
      </div>
      <div className="flex flex-wrap gap-x-2">
        <dt className="text-muted">Last status check</dt>
        <dd>{formatAgo(s.checked_at)}{s.stale && <> <Badge tone="warn">stale</Badge></>}
          {!s.reachable && s.last_error && <span className="text-danger"> · failed: {s.last_error}</span>}</dd>
      </div>
      <div className="flex flex-wrap gap-x-2">
        <dt className="text-muted">Uptime</dt>
        <dd>{formatUptime(s.uptime_s)}{s.stale && <span className="text-muted"> (at the last check)</span>}</dd>
      </div>
    </dl>
  );
}

/** One small summary per area of the workspace, each opening it. */
function AreaTiles({ h }: { h: Host }) {
  const { can } = useAuth();
  const tiles: ReactNode[] = [];
  if (can("desired.read")) tiles.push(<DesiredTile key="desired" h={h} />);
  if (can("firewall.read")) tiles.push(<FirewallTile key="firewall" h={h} />);
  if (can("network.read")) tiles.push(<NetworkTile key="network" h={h} />);
  if (can("files.read")) tiles.push(<FilesTile key="files" h={h} />);
  if (tiles.length === 0) return null;
  return <div className={cx("grid gap-4 sm:grid-cols-2", tiles.length >= 4 ? "xl:grid-cols-4" : tiles.length === 3 ? "xl:grid-cols-3" : "")}>{tiles}</div>;
}

function Tile({ title, to, children, testId }: { title: string; to: string; children: ReactNode; testId?: string }) {
  return (
    <section className="flex flex-col rounded-ui border border-line bg-surface p-4" data-testid={testId}>
      <header className="mb-2 flex items-baseline justify-between gap-2">
        <h2 className="text-sm font-semibold">{title}</h2>
        <ViewLink to={to} label={`Open ${title}`}>Open</ViewLink>
      </header>
      <div className="space-y-1 text-sm">{children}</div>
    </section>
  );
}

function DesiredTile({ h }: { h: Host }) {
  const q = useDesiredHost(h.id);
  const d = q.data;
  const problems = (d?.resources ?? []).filter((r) => r.status !== "compliant").slice(0, 3);
  return (
    <Tile title="Desired State" to={hostPath(h.id, "desired")} testId="tile-desired">
      {q.error ? <p className="text-danger">Couldn&apos;t load: {errorMessage(q.error)}</p> : !d ? <Loading /> : d.counts.total === 0 ? (
        <p className="text-muted">No baseline applies to this host.</p>
      ) : (
        <>
          <p className="flex flex-wrap items-center gap-2"><DesiredStatusBadge status={d.status} /><span>{d.counts.compliant} of {d.counts.total} compliant</span></p>
          {(d.counts.drift > 0 || d.counts.conflict > 0 || d.counts.failed > 0 || d.counts.unknown > 0) && (
            <p className="text-xs">
              {[d.counts.drift && <span key="d" className="text-warn">{d.counts.drift} drifted</span>,
                d.counts.conflict && <span key="c" className="text-danger">{d.counts.conflict} in conflict</span>,
                d.counts.failed && <span key="f" className="text-danger">{d.counts.failed} failed to restore</span>,
                d.counts.unknown && <span key="u" className="text-muted">{d.counts.unknown} couldn&apos;t be checked</span>]
                .filter(Boolean).flatMap((x, i) => (i ? [", ", x] : [x]))}
            </p>
          )}
          {problems.length > 0 && (
            <ul className="space-y-0.5 text-xs">
              {problems.map((r) => <li key={r.key} className="truncate"><span className="font-mono">{describeKey(r.key)}</span> — {statusLabel(r.status).toLowerCase()}</li>)}
            </ul>
          )}
        </>
      )}
    </Tile>
  );
}

function FirewallTile({ h }: { h: Host }) {
  const q = useFwHost(h.id);
  const p = q.data;
  const st = p ? fwStatusLabels[p.summary.status] : undefined;
  return (
    <Tile title="Firewall" to={hostPath(h.id, "firewall")} testId="tile-firewall">
      {q.error ? <p className="text-danger">Couldn&apos;t load: {errorMessage(q.error)}</p> : !p || !st ? <Loading /> : (
        <>
          <p className="flex flex-wrap items-center gap-2"><Badge tone={st.tone}>{st.text}</Badge><span>{fwModes[p.config.mode]?.label ?? p.config.mode}</span></p>
          <p className="text-xs text-muted">{!p.applied.enforced ? "DROOL isn't filtering this host"
            : p.applied.drift === "missing" ? "DROOL's rules are missing on the host" : `Revision ${p.applied.revision} enforced`}</p>
          {p.applied.drift && <p className="text-xs text-danger">Changed outside DROOL (drift)</p>}
        </>
      )}
    </Tile>
  );
}

function NetworkTile({ h }: { h: Host }) {
  const q = useHostNetwork(h.id);
  const n = q.data;
  const enforced = n?.firewall.enforcement === "enforced";
  return (
    <Tile title="Network" to={hostPath(h.id, "network")} testId="tile-network">
      {q.error ? <p className="text-danger">Couldn&apos;t load: {errorMessage(q.error)}</p> : !n ? <Loading /> : (
        <>
          <p><CollectionBadge c={n.collection} /></p>
          <p className="text-xs">
            {n.counts.flows} connection{n.counts.flows === 1 ? "" : "s"} observed
            {/* While DROOL doesn't filter here, nothing is blocked: what
                isn't in a policy yet is information, not a warning. */}
            {n.counts.unclassified > 0 && <> · <span className={enforced ? "text-warn" : "text-muted"}>{n.counts.unclassified} not in policy</span></>}
            {n.counts.denied > 0 && <> · <span className={enforced ? "text-danger" : "text-muted"}>{n.counts.denied} {enforced ? "denied" : "would be denied"}</span></>}
          </p>
        </>
      )}
    </Tile>
  );
}

function FilesTile({ h }: { h: Host }) {
  const q = useQuery({
    queryKey: ["managed-files", h.id],
    queryFn: () => unwrap(api.GET("/hosts/{id}/managed-files", { params: { path: { id: h.id } } })),
  });
  const items = q.data?.items ?? [];
  const failed = items.filter((d) => !["installed", "unchanged", "removed"].includes(d.outcome)).length;
  return (
    <Tile title="Files" to={hostPath(h.id, "files")} testId="tile-files">
      {q.error ? <p className="text-danger">Couldn&apos;t load: {errorMessage(q.error)}</p> : !q.data ? <Loading /> : items.length === 0 ? (
        <p className="text-muted">No files deployed by DROOL. Browse and edit files on the host.</p>
      ) : (
        <>
          <p>{items.length} file{items.length === 1 ? "" : "s"} deployed by DROOL{failed > 0 && <span className="text-danger"> · {failed} failed</span>}</p>
          <ul className="space-y-1 text-xs">
            {items.slice(0, 3).map((d) => <li key={d.id} className="truncate"><Link className={cx(plainLink, "inline-block py-0.5")} to={`/file-deployments/${d.id}`}>{d.path}</Link></li>)}
          </ul>
        </>
      )}
    </Tile>
  );
}

function RecentRuns({ h }: { h: Host }) {
  const jobs = useJobs(h.id);
  return (
    <Card title="Recent runs" actions={<ViewLink to={hostPath(h.id, "activity")}>Activity</ViewLink>}>
      {jobs.error ? <LoadError what="runs" error={jobs.error} /> : !jobs.data ? <Loading /> : jobs.data.items.length === 0 ? (
        <p className="text-sm text-muted">No runs have included this host yet.</p>
      ) : (
        <ul className="space-y-2 text-sm">
          {jobs.data.items.slice(0, 5).map((j) => (
            <li key={j.id} className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
              <Link className="min-w-0 flex-1 truncate text-accent" to={`/jobs/${j.id}`}>{j.name}</Link>
              <JobStatusBadge job={j} />
              <span className="w-full text-xs text-muted">{formatDateTime(j.created_at)} · {j.created_by}</span>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}

/** A jump host isn't managed itself: what it is for, and who uses it. */
function JumpHostNote({ h }: { h: Host }) {
  const hosts = useHosts();
  const behind = (hosts.data?.items ?? []).filter((x) => x.via_host_id === h.id);
  return (
    <Card title="Jump host">
      <p className="text-sm">DROOL connects through {h.name} to reach other hosts; it doesn&apos;t manage {h.name} itself, so it has no monitoring,
        runs or files here. Its connection is under <Link className={plainLink} to={hostPath(h.id, "management")}>Management</Link>.</p>
      {behind.length > 0 && (
        <p className="mt-2 text-sm">Reached through it: {behind.map((b, i) => <span key={b.id}>{i > 0 && ", "}<Link className={plainLink} to={`/hosts/${b.id}`}>{b.name}</Link></span>)}</p>
      )}
    </Card>
  );
}
