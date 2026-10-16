import { useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { qk, useCredentials, useGroups, useHosts, type Host } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { ConnectionCard, ShepherdCard } from "@/components/agents/ManagementCard";
import { HostForm, type HostRequest } from "@/components/hosts/HostForm";
import { RemoveHostDialog } from "@/components/hosts/RemoveHostDialog";
import { SSHSignIn } from "@/components/hosts/SSHSignIn";
import { TestConnectionDialog } from "@/components/hosts/TestConnectionDialog";
import { useHostWorkspace } from "@/components/hosts/workspace/HostLayout";
import { HostPacksDialog } from "@/components/packs/PackDialogs";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { SectionHeader } from "@/components/ui/SectionHeader";
import { plainLink } from "@/components/ui/links";
import { errorMessage, formatDateTime } from "@/lib/format";

/** How DROOL reaches and manages one host, by purpose: the connection
 * (which path, its fallback, whether operations run), SSH access (sign-in,
 * credential, DROOL's own account, certificate trust), the Shepherd, what
 * the host belongs to, and removing it. */
export function HostManagementPage() {
  const { host: h } = useHostWorkspace();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const { can } = useAuth();
  const [params, setParams] = useSearchParams();
  const [removing, setRemoving] = useState(false);
  const [testing, setTesting] = useState(false);
  const editing = params.get("edit") === "1" && can("hosts.write");
  const setEditing = (on: boolean) => setParams((p) => {
    if (on) p.set("edit", "1");
    else p.delete("edit");
    return p;
  }, { replace: true });

  const update = useMutation({
    mutationFn: (body: HostRequest) => unwrap(api.PATCH("/hosts/{id}", { params: { path: { id: h.id } }, body })),
    onSuccess: () => {
      setEditing(false);
      void qc.invalidateQueries({ queryKey: qk.host(h.id) });
      void qc.invalidateQueries({ queryKey: qk.hosts });
      void qc.invalidateQueries({ queryKey: qk.hostKeys(h.id) });
    },
  });
  const sshHost = h.management.ssh_configured;
  const needsKey = sshHost && (h.status === "pending_hostkey" || h.status === "hostkey_mismatch");
  const managed = !h.jump_only;

  return (
    <div className="space-y-4" data-testid="host-management">
      <SectionHeader title="Management"
        description="How DROOL reaches this host — SSH, its Shepherd (DROOL agent) or both — and what it belongs to."
        actions={can("hosts.write") && !editing && <Button onClick={() => setEditing(true)}>Edit host details</Button>} />
      {editing && (
        <Card title="Edit host">
          {update.error && <div className="mb-4"><Alert>{errorMessage(update.error)}</Alert></div>}
          <HostForm host={h} submitLabel="Save" busy={update.isPending} onSubmit={(b) => update.mutate(b)} onCancel={() => setEditing(false)} />
        </Card>
      )}
      <div className="grid gap-4 xl:grid-cols-2">
        <div className="space-y-4">
          <ConnectionCard host={h}>
            {can("jobs.execute") && sshHost && (
              <Button size="sm" onClick={() => setTesting(true)} disabled={needsKey} title={needsKey ? "Approve the host key first (Overview)" : undefined}>
                Test connection
              </Button>
            )}
          </ConnectionCard>
          {sshHost && <SSHAccessCard h={h} />}
        </div>
        <div className="space-y-4">
          {managed && <ShepherdCard host={h} />}
          <MembershipCard h={h} />
        </div>
      </div>
      {can("hosts.write") && (
        <Card title="Danger zone">
          <div className="flex flex-wrap items-center gap-3">
            <Button variant="danger" onClick={() => setRemoving(true)}>Remove host…</Button>
            <span className="text-xs text-muted">Remove it from DROOL only (nothing on the host changes), or first clean up what DROOL installed there: its Shepherd, and the SSH account DROOL created.</span>
          </div>
          {removing && (
            <RemoveHostDialog hostId={h.id} hostName={h.name} onClose={() => setRemoving(false)}
              onRemoved={() => {
                void qc.invalidateQueries({ queryKey: qk.hosts });
                navigate("/hosts");
              }} />
          )}
        </Card>
      )}
      {testing && <TestConnectionDialog host={h} onClose={() => setTesting(false)} />}
    </div>
  );
}

/** SSH: who DROOL signs in as, with which credential (a link to it), sudo,
 * the route, the trusted host key, and how it signs in (credential or
 * certificates, DROOL's own account). */
function SSHAccessCard({ h }: { h: Host }) {
  const { can } = useAuth();
  const hosts = useHosts();
  const creds = useCredentials(can("credentials.use"));
  const needsKey = h.status === "pending_hostkey" || h.status === "hostkey_mismatch";
  const nameOf = (hid?: string | null) => (hosts.data?.items ?? []).find((x) => x.id === hid)?.name;
  // A role without credentials.use can't list credentials: say so rather than show an internal ID.
  const credName = (cid?: string | null) => !cid ? cid : (creds.data?.items ?? []).find((c) => c.id === cid)?.name
    ?? (!can("credentials.use") ? "a stored credential — your role can't see its name" : creds.data ? cid : "…");
  return (
    <Card title="SSH access">
      <dl className="grid grid-cols-[minmax(7rem,9rem)_1fr] gap-y-1.5 text-sm" data-testid="host-ssh">
        <dt className="text-muted">SSH</dt><dd className="min-w-0 break-all"><span className="font-mono text-xs">{h.user}@{h.address}:{h.port}</span></dd>
        <dt className="text-muted">Credential</dt>
        <dd>
          {h.credential_id && can("credentials.use") ? <Link className="text-accent" to="/credentials">{credName(h.credential_id)}</Link> : credName(h.credential_id) || "—"}
          {h.ssh_auth === "certificate" && h.credential_id && <span className="text-muted"> (kept as a way back; DROOL signs in with certificates)</span>}
        </dd>
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
          ) : <span className="text-warn">{h.status === "hostkey_mismatch" ? "changed — not trusted" : "not approved yet"} <Link className={plainLink} to={`/hosts/${h.id}`}>(Overview)</Link></span>}
        </dd>
        <dt className="text-muted">Last test</dt><dd>{formatDateTime(h.last_probe_at)}</dd>
      </dl>
      <SSHSignIn host={h} />
    </Card>
  );
}

/** What the host belongs to and how it is described: Packs (which bring
 * inherited settings), tags and description. */
function MembershipCard({ h }: { h: Host }) {
  const { can } = useAuth();
  const groups = useGroups();
  const [packsOpen, setPacksOpen] = useState(false);
  const packs = (groups.data?.items ?? []).filter((g) => h.group_ids.includes(g.id));
  return (
    <Card title="Membership">
      <dl className="grid grid-cols-[minmax(7rem,9rem)_1fr] gap-y-1.5 text-sm" data-testid="host-membership">
        <dt className="text-muted">Packs <span className="text-xs">(groups)</span></dt>
        <dd>
          {packs.length ? packs.map((g, i) => <span key={g.id}>{i > 0 && ", "}<Link className="text-accent" to={`/groups/${g.id}`}>{g.name}</Link></span>) : "—"}
          {can("hosts.write") && <button type="button" className={`ml-2 text-xs ${plainLink}`} onClick={() => setPacksOpen(true)}>Change…</button>}
          {packsOpen && <HostPacksDialog host={h} packs={groups.data?.items ?? []} onClose={() => setPacksOpen(false)} />}
        </dd>
        <dt className="text-muted">Tags</dt>
        <dd className="flex flex-wrap gap-1">{h.tags.length ? h.tags.map((t) => <Badge key={t}>{t}</Badge>) : "—"}</dd>
        <dt className="text-muted">Description</dt><dd>{h.description || "—"}</dd>
        {h.jump_only && (<><dt className="text-muted">Role</dt><dd>Jump host only</dd></>)}
      </dl>
      <p className="mt-3 text-xs text-muted">A Pack&apos;s settings (monitoring, desired state, firewall policy) apply to its hosts; tags and Packs both work as run and schedule targets.</p>
    </Card>
  );
}
