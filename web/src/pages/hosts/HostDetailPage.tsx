import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { qk, useCredentials, useGroups, useHost, useHosts, type ProbeResult } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { HostForm, type HostRequest } from "@/components/hosts/HostForm";
import { HostKeyPanel } from "@/components/hosts/HostKeyPanel";
import { HostStatusBadge } from "@/components/hosts/HostStatusBadge";
import { ProbeSteps } from "@/components/hosts/ProbeSteps";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { PageHeader } from "@/components/ui/PageHeader";
import { errorMessage, formatDateTime } from "@/lib/format";

export function HostDetailPage() {
  const { id = "" } = useParams();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const { can } = useAuth();
  const host = useHost(id);
  const hosts = useHosts();
  const groups = useGroups();
  const creds = useCredentials(can("credentials.use"));
  const [editing, setEditing] = useState(false);
  const [probe, setProbe] = useState<ProbeResult>();

  const refresh = () => {
    void qc.invalidateQueries({ queryKey: qk.host(id) });
    void qc.invalidateQueries({ queryKey: qk.hosts });
    void qc.invalidateQueries({ queryKey: qk.hostKeys(id) });
  };
  const update = useMutation({
    mutationFn: (body: HostRequest) => unwrap(api.PATCH("/hosts/{id}", { params: { path: { id } }, body })),
    onSuccess: () => {
      setEditing(false);
      refresh();
    },
  });
  const test = useMutation({
    mutationFn: () => unwrap(api.POST("/hosts/{id}/probe", { params: { path: { id } } })),
    onSuccess: (r) => {
      setProbe(r);
      refresh();
    },
  });
  const remove = useMutation({
    mutationFn: () => unwrap(api.DELETE("/hosts/{id}", { params: { path: { id } } })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.hosts });
      navigate("/hosts");
    },
  });

  if (host.error) return <Alert>{errorMessage(host.error)}</Alert>;
  const h = host.data;
  if (!h) return <p className="text-muted">Loading…</p>;

  const nameOf = (hid?: string | null) => (hosts.data?.items ?? []).find((x) => x.id === hid)?.name;
  const credName = (cid?: string | null) => (creds.data?.items ?? []).find((c) => c.id === cid)?.name ?? cid;
  const groupNames = (groups.data?.items ?? []).filter((g) => h.group_ids.includes(g.id)).map((g) => g.name);
  const needsKey = h.status === "pending_hostkey" || h.status === "hostkey_mismatch";

  return (
    <>
      <PageHeader
        title={h.name}
        description={<span className="inline-flex items-center gap-2"><HostStatusBadge status={h.status} /> {h.status_detail}</span>}
        actions={
          <>
            {can("jobs.execute") && (
              <Button variant="primary" onClick={() => test.mutate()} disabled={test.isPending || needsKey}>
                {test.isPending ? "Testing…" : "Test connection"}
              </Button>
            )}
            {can("hosts.write") && !editing && <Button onClick={() => setEditing(true)}>Edit</Button>}
          </>
        }
      />
      <div className="grid gap-4 lg:grid-cols-2">
        {editing ? (
          <Card title="Edit host" className="lg:col-span-2">
            {update.error && <div className="mb-4"><Alert>{errorMessage(update.error)}</Alert></div>}
            <HostForm host={h} submitLabel="Save" busy={update.isPending} onSubmit={(b) => update.mutate(b)}
              onCancel={() => setEditing(false)} />
          </Card>
        ) : (
          <Card title="Connection">
            <dl className="grid grid-cols-[9rem_1fr] gap-y-2 text-sm">
              <dt className="text-muted">Address</dt><dd className="font-mono text-xs">{h.address}:{h.port}</dd>
              <dt className="text-muted">User</dt><dd>{h.user}</dd>
              <dt className="text-muted">Credential</dt><dd>{credName(h.credential_id)}</dd>
              <dt className="text-muted">Privilege</dt>
              <dd>{h.become_method === "sudo" ? (h.become_credential_id ? `sudo (password: ${credName(h.become_credential_id)})` : "passwordless sudo") : "none"}</dd>
              <dt className="text-muted">Connect through</dt>
              <dd>{h.via_host_id ? <Link className="text-accent" to={`/hosts/${h.via_host_id}`}>{nameOf(h.via_host_id)}</Link> : "direct"}</dd>
              {h.jump_only && (<><dt className="text-muted">Role</dt><dd>Jump host only</dd></>)}
              <dt className="text-muted">Last test</dt><dd>{formatDateTime(h.last_probe_at)}</dd>
              <dt className="text-muted">Tags</dt>
              <dd className="flex flex-wrap gap-1">{h.tags.length ? h.tags.map((t) => <Badge key={t}>{t}</Badge>) : "—"}</dd>
              <dt className="text-muted">Groups</dt><dd>{groupNames.length ? groupNames.join(", ") : "—"}</dd>
              {h.description && (<><dt className="text-muted">Description</dt><dd>{h.description}</dd></>)}
            </dl>
          </Card>
        )}

        <Card title="Host key">
          {needsKey || !h.pinned_key ? (
            <HostKeyPanel hostId={h.id} onApproved={() => test.mutate()} />
          ) : (
            <div className="space-y-3 text-sm">
              <div>
                <div className="text-xs text-muted">{h.pinned_key.type}</div>
                <code className="break-all font-mono text-xs">{h.pinned_key.fingerprint}</code>
              </div>
              <p className="text-muted">Trusted {formatDateTime(h.pinned_key.decided_at)}.</p>
            </div>
          )}
        </Card>

        {(probe || test.error) && (
          <Card title="Connection test" className="lg:col-span-2">
            {test.error ? <Alert>{errorMessage(test.error)}</Alert> : probe && <ProbeSteps result={probe} />}
          </Card>
        )}

        {can("hosts.write") && (
          <Card title="Danger zone" className="lg:col-span-2">
            {remove.error && <div className="mb-3"><Alert>{errorMessage(remove.error)}</Alert></div>}
            <Button variant="danger" disabled={remove.isPending}
              onClick={() => { if (confirm(`Remove ${h.name} from DROOL? Nothing is changed on the host.`)) remove.mutate(); }}>
              Remove host
            </Button>
          </Card>
        )}
      </div>
    </>
  );
}
