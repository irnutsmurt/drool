import { useState, type FormEvent } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { qk, useGroups, useHosts, type Group } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { Checkbox } from "@/components/ui/Checkbox";
import { Field, Input } from "@/components/ui/Field";
import { PageHeader } from "@/components/ui/PageHeader";
import { errorMessage } from "@/lib/format";

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

  return (
    <>
      <PageHeader title="Groups" description="Named sets of hosts to target together." />
      {err && <div className="mb-4"><Alert>{errorMessage(err)}</Alert></div>}
      {can("hosts.write") && (
        <Card className="mb-4 max-w-xl">
          <form onSubmit={submit} className="flex items-end gap-3">
            <div className="flex-1">
              <Field label="New group"><Input value={name} placeholder="web-servers" required onChange={(e) => setName(e.target.value)} /></Field>
            </div>
            <Button type="submit" variant="primary" disabled={create.isPending}>Create</Button>
          </form>
        </Card>
      )}
      <div className="grid gap-4 md:grid-cols-2">
        {(groups.data?.items ?? []).map((g: Group) => (
          <Card key={g.id} title={`${g.name} (${g.host_ids.length})`}
            actions={can("hosts.write") && (
              <div className="flex gap-2">
                <Button size="sm" onClick={() => setEditing(editing === g.id ? undefined : g.id)}>
                  {editing === g.id ? "Close" : "Members"}
                </Button>
                <Button size="sm" variant="danger"
                  onClick={() => { if (confirm(`Delete group ${g.name}? Hosts are not affected.`)) remove.mutate(g.id); }}>
                  Delete
                </Button>
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
              <p className="text-sm text-muted">{g.host_ids.length ? g.host_ids.map(hostName).join(", ") : "No members."}</p>
            )}
          </Card>
        ))}
      </div>
    </>
  );
}
