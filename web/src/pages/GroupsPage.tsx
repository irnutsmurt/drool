import { useState } from "react";
import { Link, useNavigate } from "react-router";
import { useGroups, useHosts, type Group } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { CreatePackDialog, DeletePackDialog } from "@/components/packs/PackDialogs";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { EmptyState } from "@/components/ui/EmptyState";
import { PageHeader } from "@/components/ui/PageHeader";
import { Loading } from "@/components/ui/States";
import { plainLink } from "@/components/ui/links";
import { errorMessage } from "@/lib/format";
import { GroupHealthButton } from "@/components/health/GlobalHealth";
import { GroupDesiredState } from "@/components/desired/GroupDesiredState";

/** Packs (host groups): named sets of hosts to target together, and the
 * level between global and per-host settings. A pack is created on its own,
 * possibly empty; its hosts are managed on its page. */
export function GroupsPage() {
  const navigate = useNavigate();
  const { can } = useAuth();
  const groups = useGroups();
  const hosts = useHosts();
  const [creating, setCreating] = useState(false);
  const [deleting, setDeleting] = useState<Group | null>(null);

  const hostName = (id: string) => (hosts.data?.items ?? []).find((h) => h.id === id)?.name ?? id;
  const err = groups.error;
  const items = groups.data?.items ?? [];
  const createButton = can("hosts.write") && <Button variant="primary" onClick={() => setCreating(true)}>Create pack</Button>;

  return (
    <>
      <PageHeader title="Packs"
        description={<>Groups of hosts used for targeting and inherited configuration. Target runs, schedules and maintenance at a pack, and give
          its hosts shared monitoring overrides, desired state and firewall settings. (A pack is what other tools call a <em>host group</em>.)</>}
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
          <Card key={g.id}
            title={<><Link className="text-accent hover:underline" to={`/groups/${g.id}`}>{g.name}</Link> ({g.host_ids.length} host{g.host_ids.length === 1 ? "" : "s"})</>}
            actions={(
              <div className="flex flex-wrap justify-end gap-2">
                {can("monitoring.read") && <GroupHealthButton groupId={g.id} name={g.name} />}
                <Button size="sm" onClick={() => navigate(`/groups/${g.id}`)} aria-label={`Open pack ${g.name}`}>
                  {can("hosts.write") ? "Manage hosts" : "Open"}
                </Button>
                {can("hosts.write") && (
                  <Button size="sm" variant="danger" onClick={() => setDeleting(g)} aria-label={`Delete pack ${g.name}`}>Delete</Button>
                )}
              </div>
            )}>
            {g.description && <p className="mb-2 text-sm text-muted">{g.description}</p>}
            <p className="text-sm">
              {g.host_ids.length ? g.host_ids.map((id, i) => (
                <span key={id}>{i > 0 && ", "}<Link className="text-accent" to={`/hosts/${id}`}>{hostName(id)}</Link></span>
              )) : <span className="text-muted">No hosts assigned yet.</span>}
            </p>
            {can("desired.read") && <GroupDesiredState groupId={g.id} />}
            {g.host_ids.length > 0 && (
              <p className="mt-3 flex flex-wrap gap-x-3 gap-y-1 border-t border-line pt-2 text-xs">
                {can("alerts.read") && <Link className={plainLink} to={`/alerts?group_id=${g.id}`}>Alerts</Link>}
                {can("jobs.execute") && <Link className={plainLink} to={`/jobs/new?groups=${g.id}`}>Run on this pack</Link>}
                {can("network.read") && <Link className={plainLink} to={`/network?group=${g.id}`}>Network Activity</Link>}
                {can("desired.read") && <Link className={plainLink} to="/desired/baselines">Baselines</Link>}
              </p>
            )}
          </Card>
        ))}
      </div>
      {creating && <CreatePackDialog onClose={() => setCreating(false)} onCreated={(g) => navigate(`/groups/${g.id}`)} />}
      {deleting && <DeletePackDialog pack={deleting} onClose={() => setDeleting(null)} onDeleted={() => setDeleting(null)} />}
    </>
  );
}
