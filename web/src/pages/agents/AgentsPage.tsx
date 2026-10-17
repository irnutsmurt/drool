import { useState } from "react";
import { Link, useSearchParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { useHosts } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { CompatibilityBadge, ManualInstallDialog } from "@/components/agents/ManagementCard";
import { capsReported, isRootEquivalent } from "@/components/agents/CapsList";
import { ak, useAgents, useEndpointMigrations, useEnrollmentTokens, usePKI, useRollouts } from "@/components/agents/queries";
import { RolloutHosts, UpdatesTab } from "@/components/agents/Updates";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { CodeBlock } from "@/components/ui/CodeBlock";
import { Field, Input } from "@/components/ui/Field";
import { PageHeader } from "@/components/ui/PageHeader";
import { Table } from "@/components/ui/Table";
import { errorMessage, formatAgo, formatDateTime } from "@/lib/format";
import { confirmAction } from "@/components/ui/ConfirmDialog";
import { TabPanel, Tabs } from "@/components/ui/Tabs";

const tabs = [
  { id: "agents", label: "Shepherds" },
  { id: "enroll", label: "Enrollment" },
  { id: "updates", label: "Updates" },
  { id: "trust", label: "Trust (PKI)" },
  { id: "endpoints", label: "Controller address" },
] as const;

export function AgentsPage() {
  const [params, setParams] = useSearchParams();
  const tab = params.get("tab") ?? "agents";
  return (
    <>
      <PageHeader title="Shepherds"
        description={<>DROOL agents installed on managed hosts: enrollment, trust, capabilities and updates. A Shepherd is the DROOL agent
          (<span className="font-mono text-xs">drool-agent</span>); it reports continuously and runs work for DROOL within the limits its host allows.</>} />
      <Tabs label="Shepherd sections" id="shepherds" className="-mx-1 mb-4 overflow-x-auto px-1" current={tab}
        tabs={tabs.map((t) => ({ key: t.id, label: t.label }))} onSelect={(k) => setParams({ tab: k })} />
      <TabPanel id="shepherds" current={tab}>
        {tab === "agents" && <AgentsTab />}
        {tab === "enroll" && <EnrollTab />}
        {tab === "updates" && <UpdatesTab />}
        {tab === "trust" && <TrustTab />}
        {tab === "endpoints" && <EndpointsTab />}
      </TabPanel>
    </>
  );
}

const updateTone = (s: string) => (s === "healthy" ? "ok" : s === "rolled_back" || s === "failed" ? "danger" : s ? "warn" : "neutral");

function AgentsTab() {
  const agents = useAgents();
  const hosts = useHosts();
  const { can } = useAuth();
  const [adding, setAdding] = useState(false);
  const name = (id: string) => (hosts.data?.items ?? []).find((h) => h.id === id)?.name ?? id;
  return (
    <>
    <Card title="Enrolled Shepherds" actions={can("agents.enroll") && <Button size="sm" variant="primary" onClick={() => setAdding(true)}>Add a host with a Shepherd</Button>}>
      <p className="mb-3 text-sm text-muted">To put an existing SSH host under a Shepherd, open the host and choose <em>Install Shepherd (DROOL agent)</em>,
        or select hosts under Monitoring and use <em>Convert to Shepherd</em>.</p>
      <Table
        rows={agents.data?.items ?? []}
        rowKey={(a) => a.id}
        empty={agents.isPending ? "Loading…" : "No Shepherds enrolled yet. Hosts without one are managed over SSH."}
        columns={[
          { header: "Host", cell: (a) => <Link className="font-medium text-accent" to={`/hosts/${a.host_id}`}>{name(a.host_id)}</Link> },
          { header: "State", cell: (a) => a.status === "quarantined" ? <Badge tone="danger">identity conflict</Badge> : !a.compatible ? <Badge tone="danger">incompatible</Badge> : a.connected ? <Badge tone="ok">connected</Badge> : <Badge tone="warn">offline {formatAgo(a.last_seen_at)}</Badge> },
          { header: "Compatibility", cell: (a) => <span title={a.compatibility.summary}><CompatibilityBadge c={a.compatibility} /></span> },
          { header: "Version", cell: (a) => <span>{a.agent_version} <span className="text-xs text-muted">proto {a.protocol || `${a.protocol_min}–${a.protocol_max}`}</span></span> },
          { header: "Update", cell: (a) => a.update_state ? <Badge tone={updateTone(a.update_state)}>{a.update_state.replace("_", " ")} {a.update_version}</Badge> : <span className="text-muted">current</span> },
          { header: "Certificate", cell: (a) => <span className="text-xs text-muted">until {formatDateTime(a.cert_not_after)}</span> },
          { header: "Capabilities", cell: (a) => !capsReported(a.effective) ? <span className="text-xs text-muted">not reported yet</span>
            : isRootEquivalent(a.effective) ? <Badge tone="danger">root-equivalent</Badge> : <Badge>limited</Badge> },
        ]}
      />
      {adding && <ManualInstallDialog onClose={() => setAdding(false)} />}
    </Card>
    <Installs />
    <UnknownAgents />
    </>
  );
}

/** Shepherds being installed over SSH in bulk (Monitoring → Convert to
 * Shepherd): a rollout of kind install, canary first. Shown while one
 * runs or is paused, and the last finished one. */
function Installs() {
  const rollouts = useRollouts();
  const qc = useQueryClient();
  const { can } = useAuth();
  const [open, setOpen] = useState<string>();
  const act = useMutation({
    mutationFn: ({ id, a }: { id: string; a: "resume" | "cancel" }) => unwrap(api.POST(`/agents/rollouts/{id}/${a}`, { params: { path: { id } } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ak.rollouts }),
  });
  const installs = (rollouts.data?.items ?? []).filter((r) => r.kind === "install");
  const shown = installs.filter((r, i) => r.status === "running" || r.status === "paused" || i === 0);
  if (shown.length === 0) return null;
  return (
    <Card title="Installing Shepherds">
      <ul className="space-y-3" data-testid="shepherd-installs">
        {shown.map((r) => (
          <li key={r.id} className="rounded-ui border border-line p-3">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <div>
                <p className="font-medium">Install Shepherd {r.version} over SSH ({r.preset})</p>
                <p className="text-sm text-muted">{r.status === "paused" ? r.paused_reason : r.status} · started {formatAgo(r.created_at)}</p>
              </div>
              <span className="flex flex-wrap gap-2">
                <Button size="sm" variant="ghost" aria-expanded={open === r.id} onClick={() => setOpen(open === r.id ? undefined : r.id)}>
                  {open === r.id ? "Hide hosts" : "Show hosts"}</Button>
                {can("agents.manage") && r.status === "paused" && <Button size="sm" onClick={() => act.mutate({ id: r.id, a: "resume" })}>Resume</Button>}
                {can("agents.manage") && (r.status === "running" || r.status === "paused") &&
                  <Button size="sm" variant="danger" onClick={() => act.mutate({ id: r.id, a: "cancel" })}>Cancel</Button>}
              </span>
            </div>
            {open === r.id && <RolloutHosts id={r.id} />}
          </li>
        ))}
      </ul>
      {act.error && <div className="mt-3"><Alert>{errorMessage(act.error)}</Alert></div>}
    </Card>
  );
}

/** Shepherds this controller has no record of (enrolled after the backup
 * it was restored from): refused, listed until dismissed. */
function UnknownAgents() {
  const { can } = useAuth();
  const qc = useQueryClient();
  const list = useQuery({ queryKey: ["agents", "unknown"], queryFn: () => unwrap(api.GET("/agents/unknown")), refetchInterval: 60_000 });
  const dismiss = useMutation({
    mutationFn: (id: string) => unwrap(api.POST("/agents/unknown/{id}/dismiss", { params: { path: { id } } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["agents", "unknown"] }),
  });
  const items = list.data?.items ?? [];
  if (items.length === 0) return null;
  return (
    <Card title="Unknown Shepherds">
      <p className="mb-3 text-sm text-muted">
        These Shepherds present a certificate from this controller&apos;s certificate authority, but an identity it has no record of —
        usually because they were enrolled after the backup this controller was restored from. They are refused. To manage the host
        again, enroll its Shepherd with a new token (Enrollment).
      </p>
      {dismiss.error && <div className="mb-3"><Alert>{errorMessage(dismiss.error)}</Alert></div>}
      <Table
        rows={items}
        rowKey={(u) => u.identity_id}
        columns={[
          { header: "Host name it reports", cell: (u) => <span className="font-medium">{u.hostname || "—"}</span> },
          { header: "Address", cell: (u) => <span className="font-mono text-xs">{u.address || "—"}</span> },
          { header: "Version", cell: (u) => u.agent_version || "—" },
          { header: "Last tried", cell: (u) => <span title={`first ${formatDateTime(u.first_seen_at)}, ${u.attempts} attempt${u.attempts === 1 ? "" : "s"}`}>{formatAgo(u.last_seen_at)}</span> },
          { header: "Identity", cell: (u) => <span className="font-mono text-xs" title={u.key_fingerprint}>{u.identity_id}</span> },
          { header: "", cell: (u) => can("agents.manage") ? <Button size="sm" variant="ghost" onClick={() => dismiss.mutate(u.identity_id)} disabled={dismiss.isPending}>Dismiss</Button> : null },
        ]}
      />
    </Card>
  );
}

function EnrollTab() {
  const tokens = useEnrollmentTokens();
  const qc = useQueryClient();
  const [adding, setAdding] = useState(false);
  const revoke = useMutation({
    mutationFn: (id: string) => unwrap(api.POST("/agents/tokens/{id}/revoke", { params: { path: { id } } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ak.tokens }),
  });
  return (
    <Card title="Enrollment tokens" actions={<Button size="sm" variant="primary" onClick={() => setAdding(true)}>New token</Button>}>
      <p className="mb-3 text-sm text-muted">Tokens are random, single use and short lived. Only a hash is stored; the token itself is shown once.</p>
      {revoke.error && <div className="mb-3"><Alert>{errorMessage(revoke.error)}</Alert></div>}
      <Table
        rows={tokens.data?.items ?? []}
        rowKey={(t) => t.id}
        empty="No tokens yet."
        columns={[
          { header: "Created", cell: (t) => formatDateTime(t.created_at) },
          { header: "For", cell: (t) => t.host_name ? `host ${t.host_name}` : t.group_name ? `new hosts in ${t.group_name}` : "new hosts" },
          { header: "Preset", cell: (t) => t.preset || "—" },
          { header: "State", cell: (t) => <Badge tone={t.state === "active" ? "accent" : t.state === "used" ? "ok" : "neutral"}>{t.state}</Badge> },
          { header: "Expires", cell: (t) => <span className="text-muted">{formatDateTime(t.expires_at)}</span> },
          { header: "", cell: (t) => t.state === "active" && <Button size="sm" variant="ghost" onClick={() => revoke.mutate(t.id)}>Revoke</Button> },
        ]}
      />
      {adding && <ManualInstallDialog onClose={() => { setAdding(false); void qc.invalidateQueries({ queryKey: ak.tokens }); }} />}
    </Card>
  );
}

function TrustTab() {
  const { can } = useAuth();
  const qc = useQueryClient();
  const pki = usePKI();
  const [exported, setExported] = useState<{ cert_pem: string; key_pem: string }>();
  const [confirmFp, setConfirmFp] = useState("");
  const [rootKey, setRootKey] = useState("");
  const [manifestJSON, setManifestJSON] = useState("");
  const inv = () => void qc.invalidateQueries({ queryKey: ak.pki });
  const exportRoot = useMutation({ mutationFn: () => unwrap(api.POST("/agents/pki/export-root")), onSuccess: setExported });
  const removeRoot = useMutation({ mutationFn: () => unwrap(api.POST("/agents/pki/remove-root", { body: { confirm: confirmFp.trim() } })), onSuccess: () => { setExported(undefined); inv(); } });
  const rotate = useMutation({
    mutationFn: (revoke: boolean) => unwrap(api.POST("/agents/pki/rotate", { body: { revoke_old: revoke, root_key_pem: rootKey.trim() || undefined } })),
    onSuccess: () => { setRootKey(""); inv(); },
  });
  const importM = useMutation({ mutationFn: () => unwrap(api.POST("/agents/pki/manifests", { body: JSON.parse(manifestJSON) })), onSuccess: () => { setManifestJSON(""); inv(); } });
  const s = pki.data;
  if (pki.error) return <Alert>{errorMessage(pki.error)}</Alert>;
  if (!s) return <p className="text-muted">Loading…</p>;
  const download = (name: string, text: string) => {
    const a = document.createElement("a");
    a.href = URL.createObjectURL(new Blob([text], { type: "application/x-pem-file" }));
    a.download = name;
    a.click();
  };
  return (
    <div className="grid gap-4 lg:grid-cols-2">
      <Card title="Agent Root CA" className="lg:col-span-2">
        {s.newest_agent_manifest > s.manifest_version && (
          <div className="mb-3"><Alert tone="warn">A Shepherd trusts a newer trust manifest (version {s.newest_agent_manifest}) than this controller has
            (version {s.manifest_version}): the intermediate CA was replaced after the backup this controller was restored from. Shepherds still connect
            while they trust this controller&apos;s intermediate; replace the intermediate below to bring them all onto one again (the new manifest
            is numbered to supersede theirs).</Alert></div>
        )}
        {s.root_key_online ? (
          <div className="mb-3"><Alert tone="warn">The root private key is stored on this controller. Anyone who compromises the controller (or
            a backup together with the master key) gets it and could mint a trust manifest. Export it, keep it offline (two copies), and remove it here.
            You only need it to replace the intermediate CA.</Alert></div>
        ) : (
          <div className="mb-3"><Alert tone="ok">The root private key is offline{s.root_key_removed_at && ` (removed ${formatDateTime(s.root_key_removed_at)})`}. It is not in this controller's backups.</Alert></div>
        )}
        <dl className="grid grid-cols-[7rem_1fr] sm:grid-cols-[11rem_1fr] gap-y-1.5 text-sm">
          <dt className="text-muted">Root fingerprint</dt><dd className="break-all font-mono text-xs">{s.root_fingerprint}</dd>
          <dt className="text-muted">Valid until</dt><dd>{formatDateTime(s.root_not_after)}</dd>
          <dt className="text-muted">Intermediate</dt><dd className="break-all font-mono text-xs">{s.intermediate_fingerprint} <span className="text-muted">(until {formatDateTime(s.intermediate_not_after)})</span></dd>
          <dt className="text-muted">Trust manifest</dt><dd>version {s.manifest_version}: {(s.trusted_intermediates ?? []).length} trusted, {(s.revoked_intermediates ?? []).length} revoked intermediate(s)
            {s.newest_agent_manifest > s.manifest_version && <span className="text-warn"> — a Shepherd has version {s.newest_agent_manifest}</span>}</dd>
          <dt className="text-muted">Agent endpoint</dt><dd><span className="font-mono">{s.advertised_endpoints.join(", ")}</span>
            <span className="text-muted"> — where Shepherds connect{s.advertised_from === "advertise" ? " (agent.advertise)" : " (this page's address)"}; DROOL listens on port {s.endpoint_port}, separate from the web UI and its certificate</span></dd>
          <dt className="text-muted">Controller epoch</dt><dd className="font-mono text-xs">{s.controller_epoch}</dd>
        </dl>
        <div className="mt-3"><a className="text-sm text-accent" href="/api/v1/agents/pki/root.crt">Download the root certificate (public)</a></div>
        {can("agents.pki") && s.root_key_online && (
          <div className="mt-4 space-y-3 border-t border-line pt-3">
            <div className="text-sm font-medium">Take the root key offline</div>
            {!exported ? (
              <Button onClick={() => exportRoot.mutate()} disabled={exportRoot.isPending}>1. Export the root key…</Button>
            ) : (
              <div className="space-y-2">
                <Alert tone="warn">This is the root private key. Save both files somewhere offline (and a second copy), then remove the key from the controller.</Alert>
                <div className="flex gap-2">
                  <Button onClick={() => download("drool-agent-root.key", exported.key_pem)}>Save drool-agent-root.key</Button>
                  <Button onClick={() => download("drool-agent-root.crt", exported.cert_pem)}>Save drool-agent-root.crt</Button>
                </div>
              </div>
            )}
            <Field label="2. Type the root fingerprint to confirm you saved the key" hint={s.root_fingerprint}>
              <Input className="font-mono text-xs" value={confirmFp} onChange={(e) => setConfirmFp(e.target.value)} />
            </Field>
            <Button variant="danger" disabled={confirmFp.trim() !== s.root_fingerprint || removeRoot.isPending} onClick={() => removeRoot.mutate()}>3. Remove the root key from the controller</Button>
            {(exportRoot.error || removeRoot.error) && <Alert>{errorMessage(exportRoot.error ?? removeRoot.error)}</Alert>}
          </div>
        )}
      </Card>

      {can("agents.pki") && (
        <Card title="Replace the intermediate CA">
          <p className="mb-3 text-sm text-muted">Signs a new intermediate and a new trust manifest with the root key. Agents take the new
            manifest before anything else when they next connect. After a compromise, revoke the old intermediate: agents move over through
            certificate recovery (identity-key proof) without re-enrolling.</p>
          {!s.root_key_online && (
            <Field label="Root private key (PEM), used once and not stored">
              <textarea className="h-24 rounded-ui border border-line bg-surface p-2 font-mono text-xs" value={rootKey} onChange={(e) => setRootKey(e.target.value)} />
            </Field>
          )}
          <div className="mt-3 flex gap-2">
            <Button disabled={rotate.isPending || (!s.root_key_online && !rootKey.trim())} onClick={() => rotate.mutate(false)}>Planned rotation</Button>
            <Button variant="danger" disabled={rotate.isPending || (!s.root_key_online && !rootKey.trim())}
              onClick={() => void confirmAction({ title: "Rotate and revoke the intermediate CA?", confirmLabel: "Rotate and revoke", danger: true,
                body: <p>For a compromised intermediate: a new one replaces it and the current one is revoked. Shepherds holding certificates from the revoked one get new ones from its replacement when they reconnect.</p> })
                .then((ok) => ok && rotate.mutate(true))}>Compromise: rotate and revoke</Button>
          </div>
          {rotate.error && <div className="mt-2"><Alert>{errorMessage(rotate.error)}</Alert></div>}
        </Card>
      )}

      {can("agents.pki") && (
        <Card title="Import a trust manifest">
          <p className="mb-3 text-sm text-muted">For manifests signed on an offline machine with <span className="font-mono">drool agent-ca sign-manifest</span>. It must be signed by this root and be newer than version {s.manifest_version}.</p>
          <textarea className="h-24 w-full rounded-ui border border-line bg-surface p-2 font-mono text-xs" value={manifestJSON} onChange={(e) => setManifestJSON(e.target.value)} placeholder='{"payload": "…", "signature": "…"}' />
          <Button className="mt-2" disabled={!manifestJSON.trim() || importM.isPending} onClick={() => importM.mutate()}>Import</Button>
          {importM.error && <div className="mt-2"><Alert>{errorMessage(importM.error)}</Alert></div>}
        </Card>
      )}

      <Card title="Backups" className="lg:col-span-2">
        <ul className="list-disc space-y-1 pl-5 text-sm">
          <li>Controller backups contain agent identities, trust manifests and the intermediate CA key (sealed with the master key) — keep the master key with them.</li>
          <li>The root key, once removed, is never in a backup. Your offline copy is the only one: you need it to recover from a lost master key or a compromised intermediate.</li>
          <li>Release-signing keys never belong on the controller or in its backups.</li>
        </ul>
        <div className="mt-2"><CodeBlock text="drool backup --include-master-key -o drool-backup.tar.gz" /></div>
      </Card>
    </div>
  );
}

function EndpointsTab() {
  const { can } = useAuth();
  const qc = useQueryClient();
  const m = useEndpointMigrations();
  const port = usePKI().data?.advertised_port;
  const [ep, setEp] = useState("");
  const propose = useMutation({ mutationFn: () => unwrap(api.POST("/agents/endpoints", { body: { endpoint: ep.trim() } })), onSuccess: () => { setEp(""); void qc.invalidateQueries({ queryKey: ak.endpoints }); } });
  const commit = useMutation({ mutationFn: (id: string) => unwrap(api.POST("/agents/endpoints/{id}/commit", { params: { path: { id } } })), onSuccess: () => void qc.invalidateQueries({ queryKey: ak.endpoints }) });
  return (
    <Card title="Controller address for Shepherds">
      <p className="mb-3 text-sm text-muted">Moving the controller or its name: propose the new address first. Every agent stores it as a candidate and tests
        it (a full mutual-TLS connection); commit makes it primary only on agents that confirmed it, and they keep the old address as a fallback.
        A wrong address can't strand an agent.{port ? ` Without a port, an address gets port ${port}, the one Shepherds are told now.` : ""}</p>
      {can("agents.manage") && (
        <div className="mb-4 flex gap-2">
          <Input className="max-w-sm font-mono" placeholder="drool.example.net" aria-label="New controller address" value={ep} onChange={(e) => setEp(e.target.value)} />
          <Button disabled={!ep.trim() || propose.isPending} onClick={() => propose.mutate()}>Propose</Button>
        </div>
      )}
      {(propose.error || commit.error) && <div className="mb-3"><Alert>{errorMessage(propose.error ?? commit.error)}</Alert></div>}
      <Table
        rows={m.data?.items ?? []}
        rowKey={(x) => x.id}
        empty="No address changes yet."
        columns={[
          { header: "Address", cell: (x) => <span className="font-mono">{x.endpoint}</span> },
          { header: "Status", cell: (x) => <Badge tone={x.status === "committed" ? "ok" : "accent"}>{x.status}</Badge> },
          { header: "Shepherds", cell: (x) => <span className="text-sm">{x.confirmed} reached it, {x.failed} could not{x.status === "committed" && `, ${x.committed} switched`} (of {x.agents})</span> },
          { header: "", cell: (x) => can("agents.manage") && x.status === "testing" && <Button size="sm" disabled={x.confirmed === 0} onClick={() => commit.mutate(x.id)}>Commit</Button> },
        ]}
      />
    </Card>
  );
}
