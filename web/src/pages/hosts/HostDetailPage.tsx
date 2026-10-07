import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { qk, useCredentials, useGroups, useHost, useHosts, useJobs, type ProbeResult } from "@/api/queries";
import { JobStatusBadge } from "@/components/jobs/badges";
import { useAuth } from "@/auth/AuthContext";
import { HostForm, type HostRequest } from "@/components/hosts/HostForm";
import { HostKeyPanel } from "@/components/hosts/HostKeyPanel";
import { HostStatusBadge } from "@/components/hosts/HostStatusBadge";
import { statusHelp } from "@/components/hosts/statusHelp";
import { FactsCard, HealthCard, NetworkCard, ServicesCard } from "@/components/hosts/InventoryCards";
import { ProbeSteps } from "@/components/hosts/ProbeSteps";
import { ManagedFilesCard } from "@/components/files/ManagedFilesCard";
import { ManagementCard } from "@/components/agents/ManagementCard";
import { FirewallCard } from "@/components/firewall/FirewallCard";
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
  const recentJobs = useJobs(id);
  const [editing, setEditing] = useState(false);
  const [probe, setProbe] = useState<ProbeResult>();

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
  const sshHost = h.management.ssh_configured;
  const needsKey = sshHost && (h.status === "pending_hostkey" || h.status === "hostkey_mismatch");

  return (
    <>
      <PageHeader
        title={h.name}
        description={<span className="inline-flex items-center gap-2"><HostStatusBadge status={h.status} /> {h.status_detail}</span>}
        actions={
          <>
            {can("jobs.execute") && sshHost && (
              <Button variant="primary" onClick={() => test.mutate()} disabled={test.isPending || needsKey}>
                {test.isPending ? "Testing…" : "Test connection"}
              </Button>
            )}
            {can("jobs.execute") && !h.jump_only && h.pinned_key && sshHost && (
              <Button onClick={() => refresh.mutate()} disabled={refresh.isPending}>
                {refresh.isPending ? "Refreshing…" : "Refresh"}
              </Button>
            )}
            {can("jobs.execute") && !h.jump_only && (
              <>
                <Button onClick={() => navigate(`/jobs/new?hosts=${h.id}`)}>Run command</Button>
                {can("files.read") && <Button onClick={() => navigate(`/hosts/${h.id}/files`)}>Files</Button>}
                <Button onClick={() => navigate(`/jobs/new?hosts=${h.id}&op=system_update`)}>Install updates</Button>
                <Button onClick={() => navigate(`/jobs/new?hosts=${h.id}&op=reboot`)}>Reboot</Button>
              </>
            )}
            {can("hosts.write") && !editing && <Button onClick={() => setEditing(true)}>Edit</Button>}
          </>
        }
      />
      {(() => {
        const help = statusHelp(h);
        return help && <div className="mb-4"><Alert tone={help.tone}>{help.text}</Alert></div>;
      })()}
      <div className="grid gap-4 lg:grid-cols-2">
        {editing ? (
          <Card title="Edit host" className="lg:col-span-2">
            {update.error && <div className="mb-4"><Alert>{errorMessage(update.error)}</Alert></div>}
            <HostForm host={h} submitLabel="Save" busy={update.isPending} onSubmit={(b) => update.mutate(b)}
              onCancel={() => setEditing(false)} />
          </Card>
        ) : !sshHost ? null : (
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

        {sshHost && <Card title="Host key">
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
        </Card>}

        {!h.jump_only && <div className="lg:col-span-2"><ManagementCard host={h} /></div>}
        {can("firewall.read") && <div className="lg:col-span-2"><FirewallCard host={h} /></div>}

        {(probe || test.error) && (
          <Card title="Connection test" className="lg:col-span-2">
            {test.error ? <Alert>{errorMessage(test.error)}</Alert> : probe && <ProbeSteps result={probe} />}
          </Card>
        )}

        {refresh.error && <div className="lg:col-span-2"><Alert>{errorMessage(refresh.error)}</Alert></div>}

        {!h.jump_only && (
          <>
            <FactsCard host={h} />
            <HealthCard host={h} />
            <div className="lg:col-span-2"><NetworkCard host={h} /></div>
            <div className="lg:col-span-2"><ServicesCard host={h} /></div>
            {can("files.read") && <div className="lg:col-span-2"><ManagedFilesCard hostId={h.id} /></div>}
          </>
        )}

        {!h.jump_only && (
          <Card title="Job history" className="lg:col-span-2">
            {(recentJobs.data?.items ?? []).length === 0 ? (
              <p className="text-sm text-muted">No jobs have run on this host yet.</p>
            ) : (
              <ul className="space-y-1.5 text-sm">
                {recentJobs.data!.items.map((j) => (
                  <li key={j.id} className="flex items-center gap-3">
                    <JobStatusBadge job={j} />
                    <Link className="text-accent" to={`/jobs/${j.id}`}>{j.name}</Link>
                    <span className="ml-auto text-muted">{formatDateTime(j.created_at)}</span>
                  </li>
                ))}
              </ul>
            )}
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
