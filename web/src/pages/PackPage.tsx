import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { qk, useGroup, useHosts, type Host } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { GroupDesiredState } from "@/components/desired/GroupDesiredState";
import { GroupHealthButton } from "@/components/health/GlobalHealth";
import { AddHostsDialog, usePackMembership } from "@/components/packs/PackDialogs";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { EmptyState } from "@/components/ui/EmptyState";
import { PageHeader } from "@/components/ui/PageHeader";
import { LoadError, Loading } from "@/components/ui/States";
import { Table } from "@/components/ui/Table";
import { plainLink } from "@/components/ui/links";
import { errorMessage } from "@/lib/format";

/** One pack: its hosts, added and removed here after the pack exists (it can
 * be empty), and what the pack is used for. */
export function PackPage() {
  const { id = "" } = useParams();
  const { can } = useAuth();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const pack = useGroup(id);
  const hosts = useHosts();
  const { remove } = usePackMembership();
  const [adding, setAdding] = useState(false);
  const del = useMutation({
    mutationFn: () => unwrap(api.DELETE("/groups/{id}", { params: { path: { id } } })),
    onSuccess: () => { void qc.invalidateQueries({ queryKey: qk.groups }); void qc.invalidateQueries({ queryKey: qk.hosts }); navigate("/groups"); },
  });

  const crumbs = [{ label: "Packs", to: "/groups" }];
  if (pack.error) return <><PageHeader crumbs={crumbs} title="Pack" /><LoadError what="the pack" error={pack.error} /></>;
  const g = pack.data;
  if (!g) return <><PageHeader crumbs={crumbs} title="Pack" /><Loading what="the pack" /></>;

  const all = hosts.data?.items ?? [];
  const members = all.filter((h) => g.host_ids.includes(h.id)).sort((a, b) => a.name.localeCompare(b.name));
  const write = can("hosts.write");
  const addButton = write && <Button variant="primary" onClick={() => setAdding(true)}>Add hosts</Button>;

  return (
    <>
      <PageHeader crumbs={crumbs} title={g.name}
        description={g.description || <span className="text-muted">A pack: hosts you run, schedule and configure together.</span>}
        actions={(
          <div className="flex flex-wrap gap-2">
            {addButton}
            {can("monitoring.read") && <GroupHealthButton groupId={g.id} name={g.name} />}
            {write && (
              <Button variant="danger" disabled={del.isPending}
                onClick={() => { if (confirm(`Delete pack ${g.name}? Its hosts are not affected.`)) del.mutate(); }}>
                Delete pack
              </Button>
            )}
          </div>
        )} />
      {(remove.error ?? del.error) && <div className="mb-4"><Alert>{errorMessage(remove.error ?? del.error)}</Alert></div>}

      <Card className="mb-4" title={`Hosts (${g.host_ids.length})`}>
        {g.host_ids.length === 0 ? (
          <EmptyState title="No hosts assigned yet"
            body={<>Add the hosts that belong in this pack. A host can be in several packs, and you can add or remove hosts whenever you
              like: the pack stays.</>}
            action={addButton || <span className="text-sm text-muted">Ask an administrator to add hosts.</span>} />
        ) : !hosts.data ? <Loading what="hosts" /> : (
          <Table<Host> rows={members} rowKey={(h) => h.id} caption={`Hosts in ${g.name}`}
            columns={[
              { header: "Host", cell: (h) => <Link className="text-accent" to={`/hosts/${h.id}`}>{h.name}</Link> },
              { header: "Address", cell: (h) => <span className="font-mono text-xs">{h.address}</span> },
              { header: "Tags", cell: (h) => <span className="flex flex-wrap gap-1">{h.tags.map((t) => <Badge key={t}>{t}</Badge>)}</span> },
              { header: "", cell: (h) => write && (
                <Button size="sm" disabled={remove.isPending} aria-label={`Remove ${h.name} from ${g.name}`}
                  onClick={() => { if (confirm(`Remove ${h.name} from ${g.name}? The host stays in DROOL; the pack's settings stop applying to it.`)) remove.mutate({ pack: g.id, host: h.id }); }}>
                  Remove
                </Button>
              ) },
            ]} />
        )}
        {can("desired.read") && <GroupDesiredState groupId={g.id} />}
      </Card>

      {g.host_ids.length > 0 && (
        <p className="flex flex-wrap gap-x-3 gap-y-1 text-sm">
          {can("jobs.execute") && <Link className={plainLink} to={`/jobs/new?groups=${g.id}`}>Run on this pack</Link>}
          {can("alerts.read") && <Link className={plainLink} to={`/alerts?group_id=${g.id}`}>Alerts</Link>}
          {can("network.read") && <Link className={plainLink} to={`/network?group=${g.id}`}>Network Activity</Link>}
          {can("desired.read") && <Link className={plainLink} to="/desired/baselines">Baselines</Link>}
        </p>
      )}

      {adding && <AddHostsDialog pack={g} hosts={all} onClose={() => setAdding(false)} />}
    </>
  );
}
