import { useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { qk, useCredentials, useGroups, useHosts } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { ManagementCard } from "@/components/agents/ManagementCard";
import { HostForm, type HostRequest } from "@/components/hosts/HostForm";
import { RemoveHostDialog } from "@/components/hosts/RemoveHostDialog";
import { SSHSignIn } from "@/components/hosts/SSHSignIn";
import { useHostWorkspace } from "@/components/hosts/workspace/HostLayout";
import { HostPacksDialog } from "@/components/packs/PackDialogs";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { SectionHeader } from "@/components/ui/SectionHeader";
import { plainLink } from "@/components/ui/links";
import { errorMessage, formatDateTime } from "@/lib/format";

/** How DROOL reaches and manages one host: its connection, SSH sign-in,
 * Shepherd, Pack membership and details, and removing it. */
export function HostManagementPage() {
  const { host: h } = useHostWorkspace();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const { can } = useAuth();
  const [params, setParams] = useSearchParams();
  const hosts = useHosts();
  const groups = useGroups();
  const creds = useCredentials(can("credentials.use"));
  const [packsOpen, setPacksOpen] = useState(false);
  const [removing, setRemoving] = useState(false);
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

  const nameOf = (hid?: string | null) => (hosts.data?.items ?? []).find((x) => x.id === hid)?.name;
  // A role without credentials.use can't list credentials: say so rather than show an internal ID.
  const credName = (cid?: string | null) => !cid ? cid : (creds.data?.items ?? []).find((c) => c.id === cid)?.name
    ?? (!can("credentials.use") ? "a stored credential — your role can't see its name" : creds.data ? cid : "…");
  const packs = (groups.data?.items ?? []).filter((g) => h.group_ids.includes(g.id));
  const sshHost = h.management.ssh_configured;
  const needsKey = sshHost && (h.status === "pending_hostkey" || h.status === "hostkey_mismatch");
  const managed = !h.jump_only;

  const connection = sshHost && (
    <dl className="grid grid-cols-[minmax(7rem,9rem)_1fr] gap-y-1.5 text-sm">
      <dt className="text-muted">SSH</dt><dd className="min-w-0 break-all"><span className="font-mono text-xs">{h.user}@{h.address}:{h.port}</span></dd>
      <dt className="text-muted">Credential</dt>
      <dd>
        {h.credential_id && can("credentials.use") ? <Link className="text-accent" to="/credentials">{credName(h.credential_id)}</Link> : credName(h.credential_id) || "—"}
        {h.ssh_auth === "certificate" && h.credential_id && <span className="text-muted"> (kept as a way back; DROOL signs in with certificates)</span>}
      </dd>
      <dt className="text-muted">Privilege</dt>
      <dd>{h.become_method === "sudo" ? (h.become_credential_id ? `sudo (password: ${credName(h.become_credential_id)})` : "passwordless sudo") : "none"}</dd>
      <dt className="text-muted">Connect through</dt>
      <dd>{h.via_host_id ? <Link className="text-accent" to={`/hosts/${h.via_host_id}/management`}>{nameOf(h.via_host_id) ?? "jump host"}</Link> : "direct"}</dd>
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
    <div className="space-y-4">
      <SectionHeader title="Management"
        description="How DROOL reaches this host — SSH, its Shepherd (DROOL agent) or both — and what it belongs to."
        actions={can("hosts.write") && !editing && <Button onClick={() => setEditing(true)}>Edit host details</Button>} />
      {editing && (
        <Card title="Edit host">
          {update.error && <div className="mb-4"><Alert>{errorMessage(update.error)}</Alert></div>}
          <HostForm host={h} submitLabel="Save" busy={update.isPending} onSubmit={(b) => update.mutate(b)} onCancel={() => setEditing(false)} />
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
    </div>
  );
}
