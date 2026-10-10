import { useState } from "react";
import { Link, useSearchParams } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { useHosts } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { ManualInstallDialog } from "@/components/agents/ManagementCard";
import { isRootEquivalent } from "@/components/agents/CapsList";
import { ak, useAgents, useEndpointMigrations, useEnrollmentTokens, usePKI, useReleaseKeys, useReleases, useRollout, useRollouts, useUpdatePolicy } from "@/components/agents/queries";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { CodeBlock } from "@/components/ui/CodeBlock";
import { cx } from "@/components/ui/cx";
import { Field, Input, Select } from "@/components/ui/Field";
import { PageHeader } from "@/components/ui/PageHeader";
import { Table } from "@/components/ui/Table";
import { errorMessage, formatAgo, formatBytes, formatDateTime } from "@/lib/format";
import { upload } from "@/lib/upload";

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
      <div role="tablist" aria-label="Shepherd sections" className="-mx-1 mb-4 flex gap-1 overflow-x-auto border-b border-line px-1">
        {tabs.map((t) => (
          <button key={t.id} role="tab" aria-selected={tab === t.id} onClick={() => setParams({ tab: t.id })}
            className={cx("-mb-px whitespace-nowrap border-b-2 px-3 py-2 text-sm", tab === t.id ? "border-accent font-medium" : "border-transparent text-muted hover:text-fg")}>
            {t.label}
          </button>
        ))}
      </div>
      {tab === "agents" && <AgentsTab />}
      {tab === "enroll" && <EnrollTab />}
      {tab === "updates" && <UpdatesTab />}
      {tab === "trust" && <TrustTab />}
      {tab === "endpoints" && <EndpointsTab />}
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
          { header: "Version", cell: (a) => <span>{a.agent_version} <span className="text-xs text-muted">proto {a.protocol || `${a.protocol_min}–${a.protocol_max}`}</span></span> },
          { header: "Update", cell: (a) => a.update_state ? <Badge tone={updateTone(a.update_state)}>{a.update_state.replace("_", " ")} {a.update_version}</Badge> : <span className="text-muted">current</span> },
          { header: "Certificate", cell: (a) => <span className="text-xs text-muted">until {formatDateTime(a.cert_not_after)}</span> },
          { header: "Capabilities", cell: (a) => isRootEquivalent(a.effective) ? <Badge tone="danger">root-equivalent</Badge> : <Badge>limited</Badge> },
        ]}
      />
      {adding && <ManualInstallDialog onClose={() => setAdding(false)} />}
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

function UpdatesTab() {
  const { can } = useAuth();
  const qc = useQueryClient();
  const policy = useUpdatePolicy();
  const releases = useReleases();
  const keys = useReleaseKeys();
  const rollouts = useRollouts();
  const [openRollout, setOpenRollout] = useState<string>();
  const [keyLine, setKeyLine] = useState("");
  const [bin, setBin] = useState<File>();
  const [manifest, setManifest] = useState<File>();
  const [progress, setProgress] = useState<number>();
  const inv = () => {
    for (const k of [ak.policy, ak.releases, ak.releaseKeys, ak.rollouts]) void qc.invalidateQueries({ queryKey: k });
  };
  const savePolicy = useMutation({
    mutationFn: (p: NonNullable<typeof policy.data>) => unwrap(api.PUT("/agents/updates/policy", { body: p })),
    onSuccess: inv,
  });
  const addKey = useMutation({
    mutationFn: () => unwrap(api.POST("/agents/release-keys", { body: { public_key: keyLine.trim() } })),
    onSuccess: () => { setKeyLine(""); inv(); },
  });
  const delKey = useMutation({ mutationFn: (id: string) => unwrap(api.DELETE("/agents/release-keys/{id}", { params: { path: { id } } })), onSuccess: inv });
  const uploadRelease = useMutation({
    mutationFn: async () => {
      const m = await manifest!.text();
      return upload("POST", "/agents/releases", bin!, setProgress, undefined, { "X-DROOL-Release": btoa(m) });
    },
    onSuccess: () => { setBin(undefined); setManifest(undefined); setProgress(undefined); inv(); },
    onError: () => setProgress(undefined),
  });
  const start = useMutation({
    mutationFn: (version: string) => unwrap(api.POST("/agents/rollouts", { body: { version } })),
    onSuccess: (r) => { setOpenRollout(r.id); inv(); },
  });
  const p = policy.data;
  const versions = [...new Set((releases.data?.items ?? []).map((r) => r.version))];
  return (
    <div className="grid gap-4 lg:grid-cols-2">
      <Card title="Update policy">
        {p && <PolicyForm key={JSON.stringify(p)} policy={p} disabled={!can("agents.manage")} onSave={(np) => savePolicy.mutate(np)} busy={savePolicy.isPending} />}
        {savePolicy.error && <div className="mt-3"><Alert>{errorMessage(savePolicy.error)}</Alert></div>}
      </Card>

      <Card title="Release-signing keys">
        <p className="mb-3 text-sm text-muted">Agents install only releases signed by a key they trust themselves (built in, or added on the host with
          <span className="font-mono"> drool-agent trust-release-key</span>). Keys listed here only let the controller check uploads early; adding one here
          does not make any agent trust it.</p>
        <ul className="mb-3 space-y-1 text-xs">
          {(keys.data?.items ?? []).map((k) => (
            <li key={k.id} className="flex items-center gap-2"><span className="font-mono">{k.id}</span> {k.name}
              {can("agents.manage") && <Button size="sm" variant="ghost" onClick={() => delKey.mutate(k.id)}>Remove</Button>}</li>
          ))}
          {(keys.data?.items ?? []).length === 0 && <li className="text-muted">None yet. Create one offline with <span className="font-mono">drool release keygen</span>.</li>}
        </ul>
        {can("agents.manage") && (
          <div className="flex gap-2">
            <Input className="font-mono text-xs" placeholder="drool-release-ed25519 AAAA… my builds" value={keyLine} onChange={(e) => setKeyLine(e.target.value)} />
            <Button disabled={!keyLine.trim() || addKey.isPending} onClick={() => addKey.mutate()}>Add</Button>
          </div>
        )}
        {addKey.error && <div className="mt-2"><Alert>{errorMessage(addKey.error)}</Alert></div>}
      </Card>

      <Card title="Releases" className="lg:col-span-2">
        {can("agents.manage") && (
          <div className="mb-4 grid gap-3 rounded-ui border border-line p-3 md:grid-cols-[1fr_1fr_auto]">
            <Field label="Agent binary"><input type="file" className="text-sm" onChange={(e) => setBin(e.target.files?.[0])} /></Field>
            <Field label="Signed manifest (.release.json)" hint="From drool release sign; the private key never comes near the controller.">
              <input type="file" accept=".json" className="text-sm" onChange={(e) => setManifest(e.target.files?.[0])} />
            </Field>
            <div className="self-end">
              <Button variant="primary" disabled={!bin || !manifest || uploadRelease.isPending} onClick={() => uploadRelease.mutate()}>
                {progress != null ? `Uploading ${Math.round(progress * 100)}%` : "Upload release"}</Button>
            </div>
          </div>
        )}
        {uploadRelease.error && <div className="mb-3"><Alert>{errorMessage(uploadRelease.error)}</Alert></div>}
        {start.error && <div className="mb-3"><Alert>{errorMessage(start.error)}</Alert></div>}
        <Table
          rows={releases.data?.items ?? []}
          rowKey={(r) => r.id}
          empty="No releases uploaded."
          columns={[
            { header: "Version", cell: (r) => <span className="font-medium">{r.version}</span> },
            { header: "Platform", cell: (r) => `${r.os}/${r.arch}` },
            { header: "Channel", cell: (r) => r.channel },
            { header: "Protocol", cell: (r) => `${r.protocol_min}–${r.protocol_max}` },
            { header: "Binary", cell: (r) => <span className="text-xs text-muted">{formatBytes(r.size)} · {r.sha256.slice(0, 12)} · key {r.key_id}</span> },
            { header: "", cell: (r) => can("agents.manage") && versions.includes(r.version) && (
              <Button size="sm" onClick={() => { if (confirm(`Roll out ${r.version} to all agents? A canary goes first; the rollout pauses on failures (see the policy).`)) start.mutate(r.version); }}>Roll out</Button>) },
          ]}
        />
      </Card>

      <Card title="Rollouts" className="lg:col-span-2">
        <Table
          rows={rollouts.data?.items ?? []}
          rowKey={(r) => r.id}
          empty="No rollouts yet."
          columns={[
            { header: "Rollout", cell: (r) => <button className="font-medium text-accent" onClick={() => setOpenRollout(r.id)}>{r.kind === "install" ? `Install agent ${r.version} over SSH (${r.preset})` : `Update to ${r.version}`}</button> },
            { header: "Status", cell: (r) => <Badge tone={r.status === "completed" ? "ok" : r.status === "paused" ? "danger" : r.status === "running" ? "accent" : "neutral"}>{r.status}</Badge> },
            { header: "Plan", cell: (r) => <span className="text-xs text-muted">canary {r.canary}, batches of {r.batch_size}, pause after {r.max_failures} failure(s){r.automatic ? " · automatic" : ""}</span> },
            { header: "Failures", cell: (r) => r.failures },
            { header: "", cell: (r) => <span className="text-xs text-muted">{r.paused_reason || formatDateTime(r.created_at)}</span> },
          ]}
        />
        {openRollout && <RolloutDetail id={openRollout} />}
      </Card>
    </div>
  );
}

function PolicyForm({ policy, disabled, onSave, busy }: {
  policy: NonNullable<ReturnType<typeof useUpdatePolicy>["data"]>;
  disabled: boolean;
  onSave: (p: typeof policy) => void;
  busy: boolean;
}) {
  const [p, setP] = useState(policy);
  const num = (k: "canary" | "batch_size" | "max_failures" | "health_window_s") => (e: React.ChangeEvent<HTMLInputElement>) => setP({ ...p, [k]: Number(e.target.value) });
  return (
    <form className="space-y-3" onSubmit={(e) => { e.preventDefault(); onSave(p); }}>
      <Field label="Policy" hint={p.policy === "automatic" ? "New releases are rolled out by themselves: canary first, then batches." : "Releases are rolled out only when someone starts a rollout."}>
        <Select value={p.policy} disabled={disabled} onChange={(e) => setP({ ...p, policy: e.target.value as "manual" | "automatic" })}>
          <option value="manual">Manual</option>
          <option value="automatic">Automatic</option>
        </Select>
      </Field>
      {p.policy === "automatic" && (
        <div className="grid grid-cols-2 gap-3">
          <Field label="Channel"><Input value={p.channel} disabled={disabled} onChange={(e) => setP({ ...p, channel: e.target.value })} /></Field>
          <Field label="Or exactly this version" hint="Empty: newest in the channel."><Input value={p.version} disabled={disabled} onChange={(e) => setP({ ...p, version: e.target.value })} /></Field>
        </div>
      )}
      <div className="grid grid-cols-2 gap-3">
        <Field label="Canary hosts"><Input type="number" min={1} value={p.canary} disabled={disabled} onChange={num("canary")} /></Field>
        <Field label="Then batches of"><Input type="number" min={1} value={p.batch_size} disabled={disabled} onChange={num("batch_size")} /></Field>
        <Field label="Pause after failures"><Input type="number" min={1} value={p.max_failures} disabled={disabled} onChange={num("max_failures")} /></Field>
        <Field label="Health window (s)" hint="A new version must connect within this time, or it is rolled back."><Input type="number" min={30} value={p.health_window_s} disabled={disabled} onChange={num("health_window_s")} /></Field>
      </div>
      {!disabled && <Button type="submit" variant="primary" disabled={busy}>Save policy</Button>}
    </form>
  );
}

const hostStateTone = (s: string) => (s === "healthy" ? "ok" : s === "rolled_back" || s === "failed" ? "danger" : s === "skipped" || s === "pending" ? "neutral" : "warn");

function RolloutDetail({ id }: { id: string }) {
  const r = useRollout(id);
  const qc = useQueryClient();
  const { can } = useAuth();
  const act = useMutation({
    mutationFn: (a: "pause" | "resume" | "cancel") => unwrap(api.POST(`/agents/rollouts/{id}/${a}`, { params: { path: { id } } })),
    onSuccess: () => { void qc.invalidateQueries({ queryKey: ak.rollout(id) }); void qc.invalidateQueries({ queryKey: ak.rollouts }); },
  });
  const ro = r.data;
  if (!ro) return null;
  return (
    <div className="mt-4 rounded-ui border border-line p-3">
      <div className="mb-2 flex items-center gap-2">
        <span className="font-medium">Rollout of {ro.version}</span>
        <Badge tone={ro.status === "paused" ? "danger" : ro.status === "completed" ? "ok" : "accent"}>{ro.status}</Badge>
        {ro.paused_reason && <span className="text-sm text-danger">{ro.paused_reason}</span>}
        <span className="ml-auto flex gap-2">
          {can("agents.manage") && ro.status === "running" && <Button size="sm" onClick={() => act.mutate("pause")}>Pause</Button>}
          {can("agents.manage") && ro.status === "paused" && <Button size="sm" onClick={() => act.mutate("resume")}>Resume</Button>}
          {can("agents.manage") && (ro.status === "running" || ro.status === "paused") && <Button size="sm" variant="danger" onClick={() => act.mutate("cancel")}>Cancel</Button>}
        </span>
      </div>
      {act.error && <Alert>{errorMessage(act.error)}</Alert>}
      <Table
        rows={ro.hosts ?? []}
        rowKey={(h) => h.host_id}
        columns={[
          { header: "Host", cell: (h) => <Link className="text-accent" to={`/hosts/${h.host_id}`}>{h.host_name}</Link> },
          { header: "Wave", cell: (h) => (h.wave === 0 ? "canary" : `batch ${h.wave}`) },
          { header: "State", cell: (h) => <><Badge tone={hostStateTone(h.state)}>{h.state.replace("_", " ")}</Badge>{h.offline && h.state === "pending" && <span className="ml-1 text-xs text-muted">offline: catches up when it reconnects</span>}</> },
          { header: "From", cell: (h) => h.from_version },
          { header: "Detail", cell: (h) => <span className="text-xs text-muted">{h.detail}</span> },
        ]}
      />
    </div>
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
          <dt className="text-muted">Trust manifest</dt><dd>version {s.manifest_version}: {(s.trusted_intermediates ?? []).length} trusted, {(s.revoked_intermediates ?? []).length} revoked intermediate(s)</dd>
          <dt className="text-muted">Agent endpoint</dt><dd>port {s.endpoint_port} (separate from the web UI and its certificate)</dd>
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
          <p className="mb-3 text-sm text-muted">Signs a new intermediate and trust manifest {s.manifest_version + 1} with the root key. Agents take the new
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
              onClick={() => { if (confirm("Revoke the current intermediate CA? Agents switch to the new one when they reconnect.")) rotate.mutate(true); }}>Compromise: rotate and revoke</Button>
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
  const [ep, setEp] = useState("");
  const propose = useMutation({ mutationFn: () => unwrap(api.POST("/agents/endpoints", { body: { endpoint: ep.trim() } })), onSuccess: () => { setEp(""); void qc.invalidateQueries({ queryKey: ak.endpoints }); } });
  const commit = useMutation({ mutationFn: (id: string) => unwrap(api.POST("/agents/endpoints/{id}/commit", { params: { path: { id } } })), onSuccess: () => void qc.invalidateQueries({ queryKey: ak.endpoints }) });
  return (
    <Card title="Controller address for Shepherds">
      <p className="mb-3 text-sm text-muted">Moving the controller or its name: propose the new address first. Every agent stores it as a candidate and tests
        it (a full mutual-TLS connection); commit makes it primary only on agents that confirmed it, and they keep the old address as a fallback.
        A wrong address can't strand an agent.</p>
      {can("agents.manage") && (
        <div className="mb-4 flex gap-2">
          <Input className="max-w-sm font-mono" placeholder="drool.example.net:9443" value={ep} onChange={(e) => setEp(e.target.value)} />
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
