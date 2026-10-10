import { useState, type FormEvent } from "react";
import { Link } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { qk, useGroups, useHosts, type Group } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { Checkbox } from "@/components/ui/Checkbox";
import { EmptyState } from "@/components/ui/EmptyState";
import { Field, Input } from "@/components/ui/Field";
import { PageHeader } from "@/components/ui/PageHeader";
import { Loading } from "@/components/ui/States";
import { plainLink } from "@/components/ui/links";
import { errorMessage } from "@/lib/format";
import { GroupHealthButton } from "@/components/health/GlobalHealth";
import { GroupDesiredState } from "@/components/desired/GroupDesiredState";

/** Packs (host groups): named sets of hosts to target together, and the
 * level between global and per-host settings. */
export function GroupsPage() {
  const qc = useQueryClient();
  const { can } = useAuth();
  const groups = useGroups();
  const hosts = useHosts();
  const [name, setName] = useState("");
  const [editing, setEditing] = useState<string>();
  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: qk.groups });
    void qc.invalidateQueries({ queryKey: qk.hosts });
  };

  const create = useMutation({
    mutationFn: () => unwrap(api.POST("/groups", { body: { name } })),
    onSuccess: (g) => {
      setName("");
      setEditing(g.id);
      invalidate();
    },
  });
  const setMembers = useMutation({
    mutationFn: (v: { id: string; host_ids: string[] }) =>
      unwrap(api.PATCH("/groups/{id}", { params: { path: { id: v.id } }, body: { host_ids: v.host_ids } })),
    onSuccess: invalidate,
  });
  const remove = useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE("/groups/{id}", { params: { path: { id } } })),
    onSuccess: invalidate,
  });

  function submit(e: FormEvent) {
    e.preventDefault();
    create.mutate();
  }

  const hostName = (id: string) => (hosts.data?.items ?? []).find((h) => h.id === id)?.name ?? id;
  const err = groups.error ?? create.error ?? setMembers.error ?? remove.error;
  const items = groups.data?.items ?? [];

  return (
    <>
      <PageHeader title="Packs"
        description={<>Groups of hosts used for targeting and inherited configuration. Target runs, schedules and maintenance at a pack, and give
          its hosts shared monitoring overrides, desired state and firewall settings. (A pack is what other tools call a <em>host group</em>.)</>} />
      {err && <div className="mb-4"><Alert>{errorMessage(err)}</Alert></div>}
      {can("hosts.write") && (
        <Card className="mb-4 max-w-xl">
          <form onSubmit={submit} className="flex flex-wrap items-end gap-3">
            <div className="min-w-[12rem] flex-1">
              <Field label="New pack"><Input value={name} placeholder="web-servers" required onChange={(e) => setName(e.target.value)} /></Field>
            </div>
            <Button type="submit" variant="primary" disabled={create.isPending}>Create pack</Button>
          </form>
        </Card>
      )}
      {!groups.data && !groups.error && <Loading what="packs" />}
      {groups.data && items.length === 0 && (
        <EmptyState title="No packs yet"
          body={<>A pack groups hosts — for example <em>web-servers</em> or <em>media</em> — so you can run, schedule and configure them together.
            A host can be in several packs; when packs disagree, their priority decides.</>}
          action={!can("hosts.write") && <span className="text-sm text-muted">Ask an administrator to create packs.</span>} />
      )}
      <div className="grid gap-4 md:grid-cols-2">
        {items.map((g: Group) => (
          <Card key={g.id} title={`${g.name} (${g.host_ids.length} host${g.host_ids.length === 1 ? "" : "s"})`}
            actions={(can("hosts.write") || can("monitoring.read")) && (
              <div className="flex flex-wrap justify-end gap-2">
                {can("monitoring.read") && <GroupHealthButton groupId={g.id} name={g.name} />}
                {can("hosts.write") && (
                  <>
                    <Button size="sm" onClick={() => setEditing(editing === g.id ? undefined : g.id)} aria-expanded={editing === g.id}>
                      {editing === g.id ? "Close" : "Members"}
                    </Button>
                    <Button size="sm" variant="danger"
                      onClick={() => { if (confirm(`Delete pack ${g.name}? Its hosts are not affected.`)) remove.mutate(g.id); }}>
                      Delete
                    </Button>
                  </>
                )}
              </div>
            )}>
            {editing === g.id ? (
              <div className="space-y-2">
                {(hosts.data?.items ?? []).map((h) => (
                  <Checkbox key={h.id} label={h.name} checked={g.host_ids.includes(h.id)}
                    onChange={(on) => setMembers.mutate({
                      id: g.id,
                      host_ids: on ? [...g.host_ids, h.id] : g.host_ids.filter((x) => x !== h.id),
                    })} />
                ))}
                {(hosts.data?.items ?? []).length === 0 && <p className="text-sm text-muted">No hosts yet.</p>}
              </div>
            ) : (
              <p className="text-sm">
                {g.host_ids.length ? g.host_ids.map((id, i) => (
                  <span key={id}>{i > 0 && ", "}<Link className="text-accent" to={`/hosts/${id}`}>{hostName(id)}</Link></span>
                )) : <span className="text-muted">No members.</span>}
              </p>
            )}
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
    </>
  );
}
