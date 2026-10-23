import { useState } from "react";
import { Link, useNavigate } from "react-router";
import { useGroups, useHosts, type Group } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { CreatePackDialog } from "@/components/packs/PackDialogs";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { EmptyState } from "@/components/ui/EmptyState";
import { PageHeader } from "@/components/ui/PageHeader";
import { Loading } from "@/components/ui/States";
import { plainLink } from "@/components/ui/links";
import { errorMessage } from "@/lib/format";
import { useFleet } from "@/components/monitoring/queries";

/** Packs (host groups): named sets of hosts to target together, and the
 * level between global and per-host settings. The list shows each Pack's
 * hosts and their health; what a Pack sets, and deleting it, are on its
 * page (one owner for each setting). */
export function GroupsPage() {
  const navigate = useNavigate();
  const { can } = useAuth();
  const groups = useGroups();
  const hosts = useHosts();
  const fleet = useFleet({ category: "all", sort: "name", limit: 500 }, can("monitoring.read"));
  const [creating, setCreating] = useState(false);
  const health = new Map((fleet.data?.rows ?? []).map((r) => [r.host_id, r]));
  // "2 healthy, 1 unhealthy": a Pack's hosts by monitoring's verdict.
  const healthLine = (ids: string[]) => {
    if (!can("monitoring.read") || ids.length === 0) return null;
    if (fleet.isLoading) return <span className="text-muted">Loading their health…</span>;
    const n = { healthy: 0, degraded: 0, unhealthy: 0, "not reporting": 0 };
    for (const id of ids) {
      const r = health.get(id);
      if (!r || r.source === "" || r.stale) n["not reporting"]++;
      else if (r.worst === "critical") n.unhealthy++;
      else if (r.worst === "warning") n.degraded++;
      else n.healthy++;
    }
    const tone: Record<string, string> = { unhealthy: "text-danger", degraded: "text-warn", "not reporting": "text-muted", healthy: "" };
    return Object.entries(n).filter(([, c]) => c > 0).map(([w, c], i) => <span key={w}>{i > 0 && ", "}<span className={tone[w]}>{c} {w}</span></span>);
  };

  const hostName = (id: string) => (hosts.data?.items ?? []).find((h) => h.id === id)?.name ?? id;
  const err = groups.error;
  const items = groups.data?.items ?? [];
  const createButton = can("hosts.write") && <Button variant="primary" onClick={() => setCreating(true)}>Create pack</Button>;

  return (
    <>
      <PageHeader title="Packs"
        description={<>Groups of hosts you run, schedule and configure together: a Pack&apos;s hosts get its health checks, monitored services,
          Desired State and firewall rules. (What other tools call a <em>host group</em>.)</>}
        actions={createButton} />
      {err && <div className="mb-4"><Alert>{errorMessage(err)}</Alert></div>}
      {!groups.data && !groups.error && <Loading what="packs" />}
      {groups.data && items.length === 0 && (
        <EmptyState title="No packs yet"
          body={<>A pack groups hosts — for example <em>web-servers</em> or <em>media</em> — so you can run, schedule and configure them together.
            Create it first, then add hosts on its page. A host can be in several packs; when packs disagree, their priority decides.</>}
          action={createButton || <span className="text-sm text-muted">Ask an administrator to create packs.</span>} />
      )}
      <div className="grid gap-4 md:grid-cols-2">
        {items.map((g: Group) => (
          <Card key={g.id} testId="pack-card"
            title={<><Link className="text-accent hover:underline" to={`/groups/${g.id}`}>{g.name}</Link> ({g.host_ids.length} host{g.host_ids.length === 1 ? "" : "s"})</>}>
            {g.description && <p className="mb-2 text-sm text-muted">{g.description}</p>}
            {g.host_ids.length > 0 && can("monitoring.read") && <p className="mb-1 text-sm" data-testid="pack-card-health">{healthLine(g.host_ids)}</p>}
            <p className="text-sm">
              {g.host_ids.length ? g.host_ids.map((id, i) => (
                <span key={id}>{i > 0 && ", "}<Link className="text-accent" to={`/hosts/${id}`}>{hostName(id)}</Link></span>
              )) : <span className="text-muted">No hosts yet: add them on the Pack&apos;s page.</span>}
            </p>
            {g.host_ids.length > 0 && (
              <p className="mt-3 flex flex-wrap gap-x-3 gap-y-1 border-t border-line pt-2 text-xs">
                {can("alerts.read") && <Link className={plainLink} to={`/alerts?group_id=${g.id}`}>Alerts</Link>}
                {can("jobs.execute") && <Link className={plainLink} to={`/jobs/new?groups=${g.id}`}>Run on this pack</Link>}
                {can("network.read") && <Link className={plainLink} to={`/network?group=${g.id}`}>Network Activity</Link>}
              </p>
            )}
          </Card>
        ))}
      </div>
      {creating && <CreatePackDialog onClose={() => setCreating(false)} onCreated={(g) => navigate(`/groups/${g.id}`)} />}
    </>
  );
}
