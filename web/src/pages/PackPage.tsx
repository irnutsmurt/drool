import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { useGroup, useHosts, type Host } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { AddHostsDialog, DeletePackDialog, usePackMembership } from "@/components/packs/PackDialogs";
import { PackConflictsCard, PackSettingsCard } from "@/components/packs/PackSettings";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { EmptyState } from "@/components/ui/EmptyState";
import { MenuButton, type MenuItem } from "@/components/ui/Menu";
import { PageHeader } from "@/components/ui/PageHeader";
import { LoadError, Loading } from "@/components/ui/States";
import { Table } from "@/components/ui/Table";
import { plainLink } from "@/components/ui/links";
import { errorMessage } from "@/lib/format";
import { ExpectedServicesCard } from "@/components/monitoring/ExpectedServices";
import { confirmAction } from "@/components/ui/ConfirmDialog";
import { InstallUpdatesDialog } from "@/components/jobs/InstallUpdatesDialog";
import { MaintenanceDialog } from "@/components/alerts/MaintenanceDialog";
import { HostStatusBadge } from "@/components/hosts/HostStatusBadge";
import { UpdatesCell } from "@/components/hosts/health";
import { HealthBadge } from "@/components/monitoring/Status";
import { useFleet, type MonFleetHost } from "@/components/monitoring/queries";

/** One Pack: its hosts and their health first (added and removed here), then
 * where another Pack decides, what the Pack sets for its hosts (each with
 * where it's changed) and its monitored services. The everyday runs are in
 * the header; maintenance and deleting the Pack under Actions, Delete last. */
export function PackPage() {
  const { id = "" } = useParams();
  const { can } = useAuth();
  const navigate = useNavigate();
  const pack = useGroup(id);
  const hosts = useHosts();
  const fleet = useFleet({ category: "all", sort: "name", limit: 500 }, can("monitoring.read"));
  const { remove } = usePackMembership();
  const [dialog, setDialog] = useState<"" | "add" | "delete" | "updates" | "maintenance">("");

  const crumbs = [{ label: "Packs", to: "/groups" }];
  if (pack.error) return <><PageHeader crumbs={crumbs} title="Pack" /><LoadError what="the Pack" error={pack.error} /></>;
  const g = pack.data;
  if (!g) return <><PageHeader crumbs={crumbs} title="Pack" /><Loading what="the Pack" /></>;

  const all = hosts.data?.items ?? [];
  const members = all.filter((h) => g.host_ids.includes(h.id)).sort((a, b) => a.name.localeCompare(b.name));
  const health = new Map((fleet.data?.rows ?? []).map((r) => [r.host_id, r]));
  const write = can("hosts.write");
  const run = can("jobs.execute") && g.host_ids.length > 0;
  const actions: MenuItem[] = [];
  if (run) actions.push({ label: "Install updates…", onSelect: () => setDialog("updates"), narrowOnly: true });
  if (can("maintenance.manage") && g.host_ids.length > 0) {
    actions.push({ label: "Start maintenance…", hint: "Pause notifications for the Pack's hosts", onSelect: () => setDialog("maintenance") });
  }
  if (write) actions.push({ label: "Delete Pack…", hint: "Its hosts stay in DROOL", danger: true, separated: actions.length > 0, onSelect: () => setDialog("delete") });

  return (
    <>
      <PageHeader crumbs={crumbs} title={g.name}
        description={g.description || <span className="text-muted">A Pack: hosts you run, schedule and configure together.</span>}
        actions={(
          <div className="flex flex-wrap gap-2">
            {run && <Button variant="primary" onClick={() => navigate(`/jobs/new?groups=${g.id}`)}>Run on this Pack</Button>}
            {run && <Button className="max-sm:hidden" onClick={() => setDialog("updates")}>Install updates…</Button>}
            {actions.length > 0 && <MenuButton label="Actions" items={actions} testId="pack-actions" />}
          </div>
        )} />
      {remove.error && <div className="mb-4"><Alert>{errorMessage(remove.error)}</Alert></div>}

      <Card className="mb-4" title={`Hosts (${g.host_ids.length})`}
        actions={write && g.host_ids.length > 0 && <Button size="sm" onClick={() => setDialog("add")}>Add hosts…</Button>}>
        {g.host_ids.length === 0 ? (
          <EmptyState title="No hosts in this Pack yet"
            body={<>Add the hosts that belong in it. They get what the Pack sets (below) and are included in what targets it. A host can be in
              several Packs, and you can add or remove hosts whenever you like: the Pack stays.</>}
            action={write ? <Button variant="primary" onClick={() => setDialog("add")}>Add hosts…</Button> : <span className="text-sm text-muted">Ask an administrator to add hosts.</span>} />
        ) : !hosts.data ? <Loading what="hosts" /> : (
          <>
            {can("monitoring.read") && <MemberSummary members={members} health={health} loading={fleet.isLoading} />}
            <Table<Host> rows={members} rowKey={(h) => h.id} caption={`Hosts in ${g.name}`}
              columns={[
                { header: "Host", cell: (h) => <Link className="font-medium text-accent" to={`/hosts/${h.id}`}>{h.name}</Link> },
                ...(can("monitoring.read") ? [{
                  header: "Health",
                  cell: (h: Host) => {
                    const r = health.get(h.id);
                    return r ? <HealthBadge hasData={r.source !== ""} stale={r.stale} issues={r.issues} worst={r.worst} />
                  : <span className="text-muted">{fleet.isError ? "couldn't load" : fleet.isLoading ? "loading…" : "unknown"}</span>;
                  },
                }] : []),
                { header: "Connection", cell: (h) => <HostStatusBadge status={h.status} /> },
                { header: "Updates", cell: (h) => <UpdatesCell host={h} /> },
                { header: "Tags", cell: (h) => <span className="flex flex-wrap gap-1">{h.tags.map((t) => <Badge key={t}>{t}</Badge>)}</span> },
                { header: "", cell: (h) => write && (
                  <Button size="sm" disabled={remove.isPending} aria-label={`Remove ${h.name} from ${g.name}`}
                    onClick={() => void confirmAction({ title: `Remove ${h.name} from ${g.name}?`, confirmLabel: "Remove from the Pack",
                      body: <p>{h.name} stays in DROOL. What the Pack sets (health checks, monitored services, Desired State, firewall rules naming the Pack) stops applying to it, and runs or schedules that target the Pack no longer include it.</p> })
                      .then((ok) => ok && remove.mutate({ pack: g.id, host: h.id }))}>
                    Remove…
                  </Button>
                ) },
              ]} />
          </>
        )}
      </Card>

      <PackConflictsCard pack={g} />
      <PackSettingsCard pack={g} />
      {can("monitoring.read") && <ExpectedServicesCard scope="group" groupId={g.id} />}

      {g.host_ids.length > 0 && (
        <p className="flex flex-wrap gap-x-3 gap-y-1 text-sm">
          {can("alerts.read") && <Link className={plainLink} to={`/alerts?group_id=${g.id}`}>Alerts for this Pack</Link>}
          {can("network.read") && <Link className={plainLink} to={`/network?group=${g.id}`}>Network Activity</Link>}
        </p>
      )}

      {dialog === "add" && <AddHostsDialog pack={g} hosts={all} onClose={() => setDialog("")} />}
      {dialog === "delete" && <DeletePackDialog pack={g} onClose={() => setDialog("")} onDeleted={() => navigate("/groups")} />}
      {dialog === "updates" && <InstallUpdatesDialog targets={{ groups: [g.id] }} label={`Pack ${g.name}`} onClose={() => setDialog("")} />}
      {dialog === "maintenance" && <MaintenanceDialog groupIds={[g.id]} defaultReason={`Work on the Pack ${g.name}`} onClose={() => setDialog("")} />}
    </>
  );
}

/** "3 hosts: 2 healthy, 1 unhealthy · 6 updates pending (2 security)": the
 * members at a glance, from monitoring's verdicts; unknown stays unknown. */
function MemberSummary({ members, health, loading }: { members: Host[]; health: Map<string, MonFleetHost>; loading: boolean }) {
  if (loading) return <p className="mb-2 text-sm text-muted">Loading their health…</p>;
  let ok = 0, degraded = 0, unhealthy = 0, unknown = 0, updates = 0, security = 0;
  for (const h of members) {
    const r = health.get(h.id);
    if (!r || r.source === "" || r.stale) unknown++;
    else if (r.worst === "critical") unhealthy++;
    else if (r.worst === "warning") degraded++;
    else ok++;
    // Old numbers from a host that isn't reporting don't add up.
    if (h.health && !h.health.stale && h.health.reachable) {
      updates += h.health.updates_available ?? 0;
      security += h.health.security_updates ?? 0;
    }
  }
  const parts = [[ok, "healthy"], [degraded, "degraded"], [unhealthy, "unhealthy"], [unknown, "not reporting"]].filter(([n]) => (n as number) > 0).map(([n, w]) => `${n} ${w}`);
  return (
    <p className="mb-2 text-sm" data-testid="pack-member-summary">
      {parts.join(", ")}{updates > 0 && <span className="text-muted"> · {updates} update{updates === 1 ? "" : "s"} pending{security ? ` (${security} security)` : ""}</span>}
    </p>
  );
}
