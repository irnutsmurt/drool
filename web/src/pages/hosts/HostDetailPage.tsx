import { useState, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { qk, useCredentials, useGroups, useHost, useHosts, useJobs, type Host, type ProbeResult } from "@/api/queries";
import { JobStatusBadge } from "@/components/jobs/badges";
import { useAuth } from "@/auth/AuthContext";
import { HostForm, type HostRequest } from "@/components/hosts/HostForm";
import { HostKeyPanel } from "@/components/hosts/HostKeyPanel";
import { HostStatusBadge } from "@/components/hosts/HostStatusBadge";
import { statusHelp } from "@/components/hosts/statusHelp";
import { attentionReasons, osLabel, ReachabilityBadge } from "@/components/hosts/health";
import { ServicesCard, StatusLines, SystemCard } from "@/components/hosts/InventoryCards";
import { ProbeSteps } from "@/components/hosts/ProbeSteps";
import { RemoveHostDialog } from "@/components/hosts/RemoveHostDialog";
import { ManagedFilesCard } from "@/components/files/ManagedFilesCard";
import { DesiredStateCard } from "@/components/desired/DesiredStateCard";
import { useDesiredHost } from "@/components/desired/queries";
import { ManagementCard } from "@/components/agents/ManagementCard";
import { SSHSignIn } from "@/components/hosts/SSHSignIn";
import { useHostAgent } from "@/components/agents/queries";
import { HostFirewallSummary } from "@/components/firewall/HostFirewall";
import { useFwHost } from "@/components/firewall/queries";
import { HostNetworkSummary } from "@/components/network/HostNetwork";
import { HostMonitoringSummary } from "@/components/monitoring/HostMonitoringSummary";
import { HostAlertsCard, HostMaintenanceBanner } from "@/components/alerts/HostAlerts";
import { useAlertSummary } from "@/components/alerts/queries";
import { Alert } from "@/components/ui/Alert";
import { HostReconciliationNotice } from "@/components/system/RestoreReconciliation";
import { HostPacksDialog } from "@/components/packs/PackDialogs";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { PageHeader } from "@/components/ui/PageHeader";
import { LoadError, Loading } from "@/components/ui/States";
import { cx } from "@/components/ui/cx";
import { plainLink } from "@/components/ui/links";
import { errorMessage, formatDateTime } from "@/lib/format";

/** "View monitoring" and friends: the way from a summary to the page that
 * has the details. */
function ViewLink({ to, children }: { to: string; children: ReactNode }) {
  return <Link className="whitespace-nowrap text-sm text-accent" to={to}>{children} <span aria-hidden="true">→</span></Link>;
}

/**
 * A host at a glance — a triage page, not the whole application for one
 * machine. It answers: is this host okay, how is DROOL managing it, and
 * what needs attention? Each area is a short summary with the way to its
 * own page (Monitoring, Desired State, Firewall, Network Activity, Runs,
 * Files); safety warnings stay in plain sight.
 */
export function HostDetailPage() {
  const { id = "" } = useParams();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const { can } = useAuth();
  const host = useHost(id);
  const hosts = useHosts();
  const groups = useGroups();
  const creds = useCredentials(can("credentials.use"));
  const recentJobs = useJobs(id);
  const [editing, setEditing] = useState(false);
  const [probe, setProbe] = useState<ProbeResult>();
  const [packsOpen, setPacksOpen] = useState(false);

  const refreshQueries = () => {
    void qc.invalidateQueries({ queryKey: qk.host(id) });
    void qc.invalidateQueries({ queryKey: qk.hosts });
    void qc.invalidateQueries({ queryKey: qk.hostKeys(id) });
  };
  const update = useMutation({
    mutationFn: (body: HostRequest) => unwrap(api.PATCH("/hosts/{id}", { params: { path: { id } }, body })),
    onSuccess: () => {
      setEditing(false);
      refreshQueries();
    },
  });
  const test = useMutation({
    mutationFn: () => unwrap(api.POST("/hosts/{id}/probe", { params: { path: { id } } })),
    onSuccess: (r) => {
      setProbe(r);
      refreshQueries();
    },
  });
  const refresh = useMutation({
    mutationFn: () => unwrap(api.POST("/hosts/{id}/refresh", { params: { path: { id } } })),
    onSuccess: () => refreshQueries(),
  });
  const [removing, setRemoving] = useState(false);
  const removed = () => {
    void qc.invalidateQueries({ queryKey: qk.hosts });
    navigate("/hosts");
  };

  const crumbs = [{ label: "Hosts", to: "/hosts" }];
  if (host.error) return <><PageHeader crumbs={crumbs} title="Host" /><LoadError what="the host" error={host.error} /></>;
  const h = host.data;
  if (!h) return <><PageHeader crumbs={crumbs} title="Host" /><Loading /></>;

  const nameOf = (hid?: string | null) => (hosts.data?.items ?? []).find((x) => x.id === hid)?.name;
  // A role without credentials.use can't list credentials: say so rather than show an internal ID.
  const credName = (cid?: string | null) => !cid ? cid : (creds.data?.items ?? []).find((c) => c.id === cid)?.name
    ?? (!can("credentials.use") ? "a stored credential — your role can't see its name" : creds.data ? cid : "…");
  const packs = (groups.data?.items ?? []).filter((g) => h.group_ids.includes(g.id));
  const sshHost = h.management.ssh_configured;
  const needsKey = sshHost && (h.status === "pending_hostkey" || h.status === "hostkey_mismatch");
  const usable = h.status === "ready" || h.status === "unsupported";
  const managed = !h.jump_only;
  const help = statusHelp(h);

  const connection = sshHost && (
    <dl className="grid grid-cols-[minmax(7rem,9rem)_1fr] gap-y-1.5 text-sm">
      <dt className="text-muted">SSH</dt><dd className="min-w-0 break-all"><span className="font-mono text-xs">{h.user}@{h.address}:{h.port}</span></dd>
      <dt className="text-muted">Credential</dt><dd>{credName(h.credential_id) || "—"}{h.ssh_auth === "certificate" && h.credential_id && <span className="text-muted"> (kept as a way back; DROOL signs in with certificates)</span>}</dd>
      <dt className="text-muted">Privilege</dt>
      <dd>{h.become_method === "sudo" ? (h.become_credential_id ? `sudo (password: ${credName(h.become_credential_id)})` : "passwordless sudo") : "none"}</dd>
      <dt className="text-muted">Connect through</dt>
      <dd>{h.via_host_id ? <Link className="text-accent" to={`/hosts/${h.via_host_id}`}>{nameOf(h.via_host_id) ?? "jump host"}</Link> : "direct"}</dd>
      <dt className="text-muted">Host key</dt>
      <dd className="min-w-0">
        {h.pinned_key && !needsKey ? (
          <>
            <span className="text-xs text-muted">{h.pinned_key.type} </span>
            <code className="break-all font-mono text-xs">{h.pinned_key.fingerprint}</code>
            <span className="block text-xs text-muted">trusted {formatDateTime(h.pinned_key.decided_at)}</span>
          </>
        ) : <span className="text-warn">{h.status === "hostkey_mismatch" ? "changed — not trusted" : "not approved yet"}</span>}
      </dd>
      <dt className="text-muted">Last test</dt><dd>{formatDateTime(h.last_probe_at)}</dd>
    </dl>
  );
  const membership = (
    <dl className="mt-3 grid grid-cols-[minmax(7rem,9rem)_1fr] gap-y-1.5 border-t border-line pt-3 text-sm">
      <dt className="text-muted">Packs <span className="text-xs">(groups)</span></dt>
      <dd>
        {packs.length ? packs.map((g, i) => <span key={g.id}>{i > 0 && ", "}<Link className="text-accent" to={`/groups/${g.id}`}>{g.name}</Link></span>) : "—"}
        {can("hosts.write") && <button type="button" className={`ml-2 text-xs ${plainLink}`} onClick={() => setPacksOpen(true)}>Change…</button>}
        {packsOpen && <HostPacksDialog host={h} packs={groups.data?.items ?? []} onClose={() => setPacksOpen(false)} />}
      </dd>
      <dt className="text-muted">Tags</dt>
      <dd className="flex flex-wrap gap-1">{h.tags.length ? h.tags.map((t) => <Badge key={t}>{t}</Badge>) : "—"}</dd>
      {h.description && (<><dt className="text-muted">Description</dt><dd>{h.description}</dd></>)}
      {h.jump_only && (<><dt className="text-muted">Role</dt><dd>Jump host only</dd></>)}
    </dl>
  );

  return (
    <>
      <PageHeader
        crumbs={crumbs}
        title={h.name}
        description={
          <span className="flex flex-col gap-1">
            <span className="inline-flex flex-wrap items-center gap-2"><HostStatusBadge status={h.status} />{h.status_detail && <span>{h.status_detail}</span>}</span>
            <span className="text-xs">
              <span className="font-mono">{h.address}{h.port !== 22 && `:${h.port}`}</span>
              {managed && <> · {osLabel(h)}</>}
              {h.jump_only && <> · jump host</>}
            </span>
          </span>
        }
        actions={
          <>
            {/* The primary action comes first: running something on a working
                host, else testing the connection. */}
            {can("jobs.execute") && managed && usable && (
              <Button variant="primary" onClick={() => navigate(`/jobs/new?hosts=${h.id}`)}>Run command</Button>
            )}
            {can("jobs.execute") && sshHost && (
              <Button variant={usable && managed ? "secondary" : "primary"} onClick={() => test.mutate()} disabled={test.isPending || needsKey}>
                {test.isPending ? "Testing…" : "Test connection"}
              </Button>
            )}
            {can("jobs.execute") && managed && !usable && (
              <Button onClick={() => navigate(`/jobs/new?hosts=${h.id}`)}>Run command</Button>
            )}
            {can("jobs.execute") && managed && (
              <>
                <Button onClick={() => navigate(`/jobs/new?hosts=${h.id}&op=system_update`)}>Install updates</Button>
                <Button onClick={() => navigate(`/jobs/new?hosts=${h.id}&op=reboot`)}>Reboot</Button>
              </>
            )}
            {can("files.read") && managed && <Button onClick={() => navigate(`/hosts/${h.id}/files`)}>Files</Button>}
            {can("jobs.execute") && managed && h.pinned_key && sshHost && (
              <Button onClick={() => refresh.mutate()} disabled={refresh.isPending} title="Collect inventory and status now">
                {refresh.isPending ? "Refreshing…" : "Refresh"}
              </Button>
            )}
            {can("hosts.write") && !editing && <Button onClick={() => setEditing(true)}>Edit</Button>}
          </>
        }
      />
      <div className="space-y-4">
        {can("alerts.read") && managed && <HostMaintenanceBanner hostId={h.id} />}
        <HostReconciliationNotice hostId={h.id} />
        {help && <Alert tone={help.tone}>{help.text}</Alert>}
        {needsKey && (
          <Card title={h.status === "hostkey_mismatch" ? "The host key changed" : "Approve the host key"}>
            <HostKeyPanel hostId={h.id} onApproved={() => test.mutate()} />
          </Card>
        )}
        {editing && (
          <Card title="Edit host">
            {update.error && <div className="mb-4"><Alert>{errorMessage(update.error)}</Alert></div>}
            <HostForm host={h} submitLabel="Save" busy={update.isPending} onSubmit={(b) => update.mutate(b)} onCancel={() => setEditing(false)} />
          </Card>
        )}
        {(probe || test.error) && (
          <Card title="Connection test">
            {test.error ? <Alert>{errorMessage(test.error)}</Alert> : probe && <ProbeSteps result={probe} />}
          </Card>
        )}
        {refresh.error && <Alert>{errorMessage(refresh.error)}</Alert>}
        {managed && <HostAttention h={h} hideStatus={!!help} />}

        <div className="grid gap-4 lg:grid-cols-2">
          {managed && (
            <Card title="Health" actions={can("monitoring.read") && <ViewLink to={`/hosts/${h.id}/monitoring`}>View monitoring</ViewLink>}>
              <div className="space-y-3">
                <p className="flex flex-wrap items-center gap-2 text-sm"><ReachabilityBadge host={h} />
                  <span className="text-muted">{h.health ? (h.health.reachable ? "answered the last status check" : "the last status check failed") : "status not checked yet"}</span></p>
                {can("monitoring.read") && <HostMonitoringSummary hostId={h.id} />}
                <StatusLines host={h} />
              </div>
            </Card>
          )}

          {managed ? (
            <ManagementCard host={h}>
              {connection && <div className="mt-3 border-t border-line pt-3">{connection}</div>}
              {sshHost && <SSHSignIn host={h} />}
              {membership}
            </ManagementCard>
          ) : (
            <Card title="Connection">
              {connection}
              {sshHost && <SSHSignIn host={h} />}
              {membership}
            </Card>
          )}

          {can("alerts.read") && managed && <HostAlertsCard hostId={h.id} />}
          {can("desired.read") && managed && <DesiredStateCard hostId={h.id} />}
          {can("firewall.read") && (
            <Card title="Firewall" actions={<ViewLink to={`/hosts/${h.id}/firewall`}>View firewall</ViewLink>}>
              <HostFirewallSummary hostId={h.id} />
            </Card>
          )}
          {can("network.read") && managed && (
            <Card title="Network Activity" actions={<ViewLink to={`/hosts/${h.id}/network`}>View network activity</ViewLink>}>
              <HostNetworkSummary hostId={h.id} />
            </Card>
          )}
          {managed && (
            <Card title="Recent runs" actions={<ViewLink to={`/jobs?host=${h.id}`}>View runs</ViewLink>}>
              {recentJobs.error ? <LoadError what="runs" error={recentJobs.error} /> : !recentJobs.data ? <Loading /> : recentJobs.data.items.length === 0 ? (
                <p className="text-sm text-muted">No runs have included this host yet.</p>
              ) : (
                <ul className="space-y-1.5 text-sm">
                  {recentJobs.data.items.slice(0, 5).map((j) => (
                    <li key={j.id} className="flex flex-wrap items-center gap-x-3 gap-y-0.5">
                      <JobStatusBadge job={j} />
                      <Link className="min-w-0 text-accent" to={`/jobs/${j.id}`}>{j.name}</Link>
                      <span className="ml-auto text-xs text-muted">{formatDateTime(j.created_at)}</span>
                    </li>
                  ))}
                </ul>
              )}
            </Card>
          )}
          {can("files.read") && managed && <ManagedFilesCard hostId={h.id} />}
          {managed && <SystemCard host={h} />}
          {managed && <ServicesCard host={h} />}
        </div>

        {can("hosts.write") && (
          <Card title="Danger zone">
            <div className="flex flex-wrap items-center gap-3">
              <Button variant="danger" onClick={() => setRemoving(true)}>Remove host…</Button>
              <span className="text-xs text-muted">Remove it from DROOL only (nothing on the host changes), or first clean up what DROOL installed there: its Shepherd, and the SSH account DROOL created.</span>
            </div>
            {removing && <RemoveHostDialog hostId={h.id} hostName={h.name} onRemoved={removed} onClose={() => setRemoving(false)} />}
          </Card>
        )}
      </div>
    </>
  );
}

interface Attention {
  key: string;
  tone: "danger" | "warn";
  text: ReactNode;
  to?: string;
  action?: string;
}

/** What needs a person on this host, across areas, most serious first —
 * honest while the answers are still loading. */
function HostAttention({ h, hideStatus }: { h: Host; hideStatus: boolean }) {
  const { can } = useAuth();
  const alerts = useAlertSummary(can("alerts.read"));
  const desired = useDesiredHost(h.id, can("desired.read"));
  const fw = useFwHost(h.id, can("firewall.read"));
  const agent = useHostAgent(h.id);
  const items: Attention[] = [];
  const status = new Set(["host key changed", "access denied", "needs host key approval", "offline"]);

  for (const r of attentionReasons(h)) {
    if (hideStatus && status.has(r)) continue;
    if (/security update/.test(r)) {
      items.push({ key: r, tone: "warn", text: r, to: can("jobs.execute") ? `/jobs/new?hosts=${h.id}&op=system_update` : undefined, action: "Install updates" });
    } else if (r === "reboot required") {
      items.push({ key: r, tone: "warn", text: "Reboot required", to: can("jobs.execute") ? `/jobs/new?hosts=${h.id}&op=reboot` : undefined, action: "Reboot" });
    } else if (/full$/.test(r)) {
      items.push({ key: r, tone: "danger", text: r, to: can("monitoring.read") ? `/hosts/${h.id}/monitoring?tab=metrics` : undefined, action: "View storage" });
    } else {
      items.push({ key: r, tone: status.has(r) ? "danger" : "warn", text: r });
    }
  }
  const a = alerts.data?.hosts[h.id];
  if (a && a.critical + a.warning > 0) {
    const open = a.critical + a.warning;
    items.push({ key: "alerts", tone: a.critical ? "danger" : "warn", to: `/alerts?host_id=${h.id}`, action: "View alerts",
      text: <>{open} open alert{open === 1 ? "" : "s"}{a.critical ? ` (${a.critical} critical)` : ""}{a.acknowledged ? `, ${a.acknowledged} acknowledged` : ""}</> });
  }
  const d = desired.data;
  if (d && d.counts.conflict + d.counts.drift + d.counts.failed > 0) {
    const parts = [d.counts.drift && `${d.counts.drift} drifted`, d.counts.conflict && `${d.counts.conflict} in conflict`, d.counts.failed && `${d.counts.failed} failed to restore`].filter(Boolean);
    items.push({ key: "desired", tone: d.counts.conflict || d.counts.failed ? "danger" : "warn", to: `/hosts/${h.id}/desired`, action: "View desired state",
      text: <>Desired State: {parts.join(", ")}</> });
  }
  const p = fw.data;
  if (p) {
    if (p.applied.drift) items.push({ key: "fw-drift", tone: "danger", to: `/hosts/${h.id}/firewall`, action: "View firewall", text: "Firewall: DROOL's rules were changed outside DROOL (drift)" });
    if ((p.policy.conflicts ?? []).length > 0) items.push({ key: "fw-mgmt", tone: "danger", to: `/hosts/${h.id}/firewall`, action: "View firewall", text: "Firewall: a policy would cut DROOL's management path (applying is refused)" });
    if (p.summary.status === "changes_pending") items.push({ key: "fw-pending", tone: "warn", to: `/hosts/${h.id}/firewall`, action: "View firewall", text: "Firewall: policy changes not applied yet" });
    if (p.pending_transaction) items.push({ key: "fw-tx", tone: "warn", to: `/hosts/${h.id}/firewall`, action: "View firewall", text: `Firewall transaction ${p.pending_transaction.state}` });
  }
  const m = agent.data?.management ?? h.management;
  const ident = agent.data?.identity;
  if (m.blocked) items.push({ key: "mgmt", tone: "danger", to: "#management", action: "Management", text: <>DROOL can&apos;t manage this host right now{m.reason ? `: ${m.reason}` : ""}</> });
  else if (m.active === "none" && m.reason) items.push({ key: "mgmt", tone: "warn", to: "#management", action: "Management", text: m.reason });
  else if (ident?.status === "quarantined") items.push({ key: "quarantine", tone: "danger", to: "#management", action: "Management", text: "Shepherd identity conflict: management is paused" });

  const loading = (can("alerts.read") && alerts.isPending) || (can("desired.read") && desired.isPending) || (can("firewall.read") && fw.isPending) || agent.isPending;
  items.sort((x, y) => (x.tone === y.tone ? 0 : x.tone === "danger" ? -1 : 1));
  if (items.length === 0) {
    return (
      <p className="rounded-ui border border-line bg-surface px-3 py-2 text-sm" role="status" data-testid="host-attention">
        {loading ? <span className="text-muted">Checking what needs attention…</span> : <><span className="text-ok" aria-hidden="true">✓ </span>Nothing needs attention on this host right now.</>}
      </p>
    );
  }
  return (
    <Card title={`Needs attention (${items.length})`}>
      <ul className="space-y-2 text-sm" data-testid="host-attention">
        {items.map((x) => (
          <li key={x.key} className="flex flex-wrap items-baseline gap-x-3 gap-y-0.5">
            <span className={cx("font-medium", x.tone === "danger" ? "text-danger" : "text-warn")}>
              <span aria-hidden="true">{x.tone === "danger" ? "✕ " : "⚠ "}</span>{x.text}
            </span>
            {x.to && (x.to.startsWith("#")
              ? <a className={plainLink} href={x.to}>{x.action}</a>
              : <Link className={plainLink} to={x.to}>{x.action}</Link>)}
          </li>
        ))}
      </ul>
      {loading && <p className="mt-2 text-xs text-muted">Still checking some areas…</p>}
    </Card>
  );
}
