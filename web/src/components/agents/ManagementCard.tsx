import { useState, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, api, unwrap, type Schemas } from "@/api/client";
import { qk, type Host } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { Checkbox } from "@/components/ui/Checkbox";
import { CodeBlock } from "@/components/ui/CodeBlock";
import { Dialog } from "@/components/ui/Dialog";
import { Field, Input } from "@/components/ui/Field";
import { errorMessage, formatAgo, formatDateTime } from "@/lib/format";
import { CapsList, capsReported } from "./CapsList";
import { ManagementBadge, stateLabels } from "./ManagementBadge";
import { PresetPicker } from "./PresetPicker";
import { ak, useHostAgent, usePKI, type AgentCaps } from "./queries";

const updateLabels: Record<string, string> = {
  offered: "update offered", staged: "update staged", updating: "updating", healthy: "updated (healthy)",
  rolled_back: "rolled back", failed: "update failed",
};

/** How DROOL manages the host: its paths (Shepherd, the DROOL agent,
 * and/or SSH; DESIGN §8), whatever the host page adds (connection, trust),
 * problems in plain sight, and the Shepherd's details one click away. */
export function ManagementCard({ host, children }: { host: Host; children?: ReactNode }) {
  const { can } = useAuth();
  const qc = useQueryClient();
  const agent = useHostAgent(host.id);
  const [dialog, setDialog] = useState<"install" | "revoke" | "caps" | "manual" | "uninstall" | null>(null);
  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: ak.hostAgent(host.id) });
    void qc.invalidateQueries({ queryKey: qk.host(host.id) });
    void qc.invalidateQueries({ queryKey: qk.hosts });
  };
  const setMgmt = useMutation({
    mutationFn: (body: { preferred: "ssh" | "agent"; fallback: "none" | "ssh" }) =>
      unwrap(api.PUT("/hosts/{id}/management", { params: { path: { id: host.id } }, body })),
    onSuccess: invalidate,
  });
  const resolve = useMutation({
    mutationFn: (id: string) => unwrap(api.POST("/agents/{id}/resolve-conflict", { params: { path: { id } } })),
    onSuccess: invalidate,
  });
  const adopt = useMutation({
    mutationFn: (domain: string) => unwrap(api.POST("/hosts/{id}/state/{domain}/adopt", { params: { path: { id: host.id, domain } } })),
    onSuccess: invalidate,
  });
  const update = useMutation({
    mutationFn: (version: string) => unwrap(api.POST("/agents/rollouts", { body: { version, host_ids: [host.id] } })),
    onSuccess: invalidate,
  });

  const a = agent.data;
  const m = a?.management ?? host.management;
  const id = a?.identity;
  const live = id && id.status !== "revoked";
  const err = setMgmt.error || resolve.error || adopt.error || update.error;

  return (
    <Card title="Management" actions={<ManagementBadge m={m} />}>
      <div id="management" className="-mt-1 scroll-mt-4" />
      {err && <div className="mb-3"><Alert>{errorMessage(err)}</Alert></div>}
      <dl className="grid grid-cols-[minmax(7rem,9rem)_1fr] gap-y-2 text-sm">
        <dt className="text-muted">Managed via</dt><dd>{m.preferred === "agent" ? "Shepherd (DROOL agent)" : "SSH"}</dd>
        {m.preferred === "agent" && (<>
          <dt className="text-muted">Shepherd</dt>
          <dd>{stateLabels[m.agent_state] ?? m.agent_state}{m.reason && m.agent_state !== "connected" && <span className="text-muted"> — {m.reason}</span>}</dd>
          <dt className="text-muted">Fallback</dt>
          <dd>{m.fallback === "ssh" ? "SSH (only while the agent is unreachable, never to work around a refusal)" : "none"}</dd>
        </>)}
        <dt className="text-muted">Operations now</dt>
        <dd>{m.active === "agent" ? "through the Shepherd" : m.active === "ssh" ? "over SSH" : <span className="text-danger">blocked</span>}</dd>
      </dl>

      {m.blocked && m.agent_state === "revoked" && (
        <div className="mt-3"><Alert>
          This host's Shepherd identity was revoked, so DROOL manages it neither through the Shepherd (DROOL agent) nor (silently) over SSH.
          Re-enroll the agent, or decide to manage the host over SSH.
          {can("hosts.write") && m.ssh_configured && (
            <div className="mt-2"><Button size="sm" onClick={() => setMgmt.mutate({ preferred: "ssh", fallback: "none" })}>Manage over SSH</Button></div>
          )}
        </Alert></div>
      )}

      {id?.status === "quarantined" && (
        <div className="mt-3"><Alert>
          <strong>Identity conflict.</strong> Two machines connected at the same time with this host's Shepherd identity — often a
          cloned VM that copied the agent's key. DROOL doesn't pick one: management is paused until you resolve it.
          <ul className="mt-2 space-y-1 text-xs">
            {(id.conflict?.sessions ?? []).map((s, i) => (
              <li key={i} className="font-mono">{s.address} · {s.hostname} · boot {s.boot_id?.slice(0, 8)} · since {formatDateTime(s.connected_at)}</li>
            ))}
          </ul>
          <p className="mt-2 text-xs">Shut down or re-image the clone (stop drool-agent and remove /var/lib/drool-agent there), then release the
            quarantine. If you can't tell which machine is genuine, revoke the identity and enroll each machine again.</p>
          {can("agents.manage") && (
            <div className="mt-2 flex gap-2">
              <Button size="sm" disabled={resolve.isPending} onClick={() => resolve.mutate(id.id)}>Release quarantine</Button>
              <Button size="sm" variant="danger" onClick={() => setDialog("revoke")}>Revoke identity</Button>
            </div>
          )}
        </Alert></div>
      )}

      {children}

      {live && id && (!id.compatible || !id.epoch_matches || (a?.sessions ?? []).length > 1 || (a?.state_versions ?? []).some((sv) => sv.status !== "ok")
        || id.update_state === "failed" || id.update_state === "rolled_back" || id.compatibility.status === "update_required" || id.compatibility.status === "reduced") && (
        <div className="mt-3 space-y-2">
          {!id.compatible && <Alert>Shepherd version {id.agent_version} isn&apos;t compatible with this controller{id.compat_reason ? `: ${id.compat_reason}` : ""}.</Alert>}
          {id.compatible && id.compatibility.status === "update_required" && <Alert>Update this Shepherd: {id.compatibility.summary}. DROOL doesn&apos;t send it operations it can&apos;t do.</Alert>}
          {id.compatibility.status === "reduced" && <Alert tone="warn">This Shepherd {id.compatibility.summary}.</Alert>}
          {!id.epoch_matches && <Alert tone="warn">The Shepherd still answers to another controller epoch ({id.epoch_reported.slice(0, 12)}), from before this controller was restored: it refuses this controller&apos;s commands until it is reconciled (see the notice above, or Settings → System → Restore).</Alert>}
          {(a?.sessions ?? []).length > 1 && <Alert>{a!.sessions.length} sessions are connected with this identity.</Alert>}
          {(a?.state_versions ?? []).some((sv) => sv.status !== "ok") && <Alert tone="warn">Managed state on the host is ahead of, or foreign to, this controller: reconcile it under Shepherd details.</Alert>}
          {(id.update_state === "failed" || id.update_state === "rolled_back") && <Alert tone="warn">The last Shepherd update {id.update_state === "failed" ? "failed" : "was rolled back"}{id.update_detail ? `: ${id.update_detail}` : ""}.</Alert>}
        </div>
      )}

      {live && id && (
        <details className="mt-4 border-t border-line pt-3">
          <summary className="cursor-pointer text-sm font-medium">
            Shepherd details <span className="font-normal text-muted">— version {id.agent_version || "—"}{a?.update_available ? `, update available: ${a.update_available}` : ""}; certificate; what DROOL may do</span>
          </summary>
          <div className="mt-3 space-y-3">
          <dl className="grid grid-cols-[minmax(7rem,9rem)_1fr] gap-y-1.5 text-sm">
            <dt className="text-muted">Shepherd version</dt>
            <dd>{id.agent_version || "—"} <span title={id.compatibility.summary}><CompatibilityBadge c={id.compatibility} /></span>
              {a?.update_available && <span className="ml-2"><Badge tone="accent">update available: {a.update_available}</Badge></span>}
              {id.update_state && <span className="ml-2"><Badge tone={id.update_state === "healthy" ? "ok" : id.update_state === "rolled_back" || id.update_state === "failed" ? "danger" : "warn"}>{updateLabels[id.update_state] ?? id.update_state}</Badge></span>}
            </dd>
            {id.update_detail && (<><dt className="text-muted">Last update</dt><dd className="text-xs text-muted">{id.update_detail}</dd></>)}
            <dt className="text-muted">Protocol</dt><dd>{id.protocol || "—"} (agent speaks {id.protocol_min}–{id.protocol_max}){id.compat_reason && <span className="text-danger"> {id.compat_reason}</span>}</dd>
            <dt className="text-muted">Identity key</dt><dd className="font-mono text-xs">{id.key_fingerprint.slice(0, 30)}…</dd>
            <dt className="text-muted">Certificate</dt><dd>valid until {formatDateTime(id.cert_not_after)} (renews after {formatDateTime(id.cert_renew_after)}){id.recovered_at && <span className="text-muted"> · recovered {formatAgo(id.recovered_at)}</span>}</dd>
            <dt className="text-muted">Trust manifest</dt><dd>version {id.manifest_version}</dd>
            <dt className="text-muted">Last seen</dt><dd>{id.connected ? "connected" : formatAgo(id.last_seen_at)} {id.last_address && <span className="font-mono text-xs text-muted">from {id.last_address}</span>}</dd>
            {!id.epoch_matches && (<><dt className="text-muted">Controller epoch</dt><dd className="text-danger">different ({id.epoch_reported.slice(0, 12)}): the agent refuses commands until reconciled</dd></>)}
          </dl>
          <div>
            <div className="mb-1 flex items-center gap-2 text-sm font-medium">What DROOL may do on this host
              {can("agents.manage") && capsReported(id.ceiling) && <Button size="sm" variant="ghost" onClick={() => setDialog("caps")}>Narrow…</Button>}</div>
            <p className="mb-2 text-xs text-muted">The host's own ceiling (/etc/drool/agent.yaml) is the limit; DROOL can narrow it but never widen it.
              {a?.requested_caps && " Narrowed by the controller."}</p>
            <CapsList caps={id.effective} />
            {a?.ssh_bypasses_ceiling && (
              <p className="mt-2 text-xs text-warn">SSH fallback logs in with root access, which this ceiling does not limit. DROOL uses SSH only while the
                agent is unreachable and never to work around the agent refusing something; remove SSH credentials (or restrict sudo) to make the ceiling the only limit.</p>
            )}
          </div>
          {(a?.state_versions ?? []).length > 0 && (
            <div>
              <div className="mb-1 text-sm font-medium">Managed state versions</div>
              <ul className="text-xs">
                {a!.state_versions.map((sv) => (
                  <li key={sv.domain} className="flex items-center gap-2">
                    <span className="font-mono">{sv.domain}</span>
                    <span className="text-muted">controller rev {sv.revision}, host rev {sv.reported_revision}</span>
                    {sv.status === "ok" ? <Badge tone="ok">in sync</Badge> : <Badge tone="danger">{sv.status === "host_ahead" ? "host ahead: reconciliation required" : "different epoch"}</Badge>}
                    {sv.status === "host_ahead" && can("agents.manage") && (
                      <Button size="sm" variant="ghost" onClick={() => { if (confirm(`Adopt the host's ${sv.domain} revision ${sv.reported_revision}? DROOL won't push older state over it.`)) adopt.mutate(sv.domain); }}>Adopt host state</Button>
                    )}
                  </li>
                ))}
              </ul>
            </div>
          )}
          {(a?.sessions ?? []).length > 1 && <p className="text-xs text-danger">{a!.sessions.length} sessions connected with this identity.</p>}
          </div>
        </details>
      )}

      <div className="mt-4 flex flex-wrap gap-2 border-t border-line pt-3">
        {!live && m.ssh_configured && can("agents.enroll") && <Button variant="primary" onClick={() => setDialog("install")}>Install Shepherd (DROOL agent)</Button>}
        {!live && can("agents.enroll") && <Button onClick={() => setDialog("manual")}>Manual install…</Button>}
        {live && a?.update_available && can("agents.manage") && (
          <Button disabled={update.isPending} onClick={() => { if (confirm(`Update the Shepherd (DROOL agent) on ${host.name} to ${a.update_available}? If it doesn't become healthy, it rolls back by itself.`)) update.mutate(a.update_available!); }}>
            Update Shepherd to {a.update_available}</Button>
        )}
        {can("hosts.write") && live && m.ssh_configured && m.preferred === "agent" && m.fallback === "ssh" && (
          <Button size="sm" variant="ghost" onClick={() => setMgmt.mutate({ preferred: "agent", fallback: "none" })}>Turn off SSH fallback</Button>
        )}
        {can("hosts.write") && live && m.ssh_configured && m.preferred === "agent" && m.fallback === "none" && (
          <Button size="sm" variant="ghost" onClick={() => setMgmt.mutate({ preferred: "agent", fallback: "ssh" })}>Use SSH as fallback</Button>
        )}
        {can("hosts.write") && live && m.preferred === "ssh" && (
          <Button size="sm" variant="ghost" onClick={() => setMgmt.mutate({ preferred: "agent", fallback: m.ssh_configured ? "ssh" : "none" })}>Prefer the Shepherd</Button>
        )}
        {can("hosts.write") && live && m.preferred === "agent" && m.ssh_configured && (
          <Button size="sm" variant="ghost" onClick={() => setMgmt.mutate({ preferred: "ssh", fallback: "none" })}>Manage over SSH only</Button>
        )}
        {id && can("agents.manage") && can("hosts.write") && (
          <Button size="sm" variant="danger" onClick={() => setDialog("uninstall")}>{live ? "Uninstall Shepherd…" : "Remove Shepherd leftovers…"}</Button>
        )}
        {live && id && can("agents.manage") && id.status !== "quarantined" && (
          <Button size="sm" variant="danger" onClick={() => setDialog("revoke")}>Revoke Shepherd identity</Button>
        )}
      </div>

      {dialog === "install" && <InstallDialog host={host} onClose={() => { setDialog(null); invalidate(); }} />}
      {dialog === "manual" && <ManualInstallDialog host={host} onClose={() => setDialog(null)} />}
      {dialog === "uninstall" && <UninstallDialog host={host} onClose={() => { setDialog(null); invalidate(); }} />}
      {dialog === "revoke" && id && <RevokeDialog identityId={id.id} hostName={host.name} onClose={() => { setDialog(null); invalidate(); }} />}
      {dialog === "caps" && id && capsReported(id.ceiling) && <CapsDialog hostId={host.id} ceiling={id.ceiling} requested={a?.requested_caps ?? null} onClose={() => { setDialog(null); invalidate(); }} />}
    </Card>
  );
}

type Preset = "monitor" | "managed" | "full";

/** Where Shepherds will connect, as the controller resolves it for this
 * page's address (the same resolver as the enrollment token): shown before
 * anything changes on a host, so a wrong address or port is caught first. */
export function ShepherdEndpointLine() {
  const pki = usePKI();
  const eps = pki.data?.advertised_endpoints ?? [];
  if (eps.length === 0) return null;
  return (
    <p className="text-sm" data-testid="shepherd-endpoint">
      Shepherds will connect to: <span className="font-mono">{eps.join(", ")}</span>
      <span className="text-muted"> (the host must be able to reach it)</span>
    </p>
  );
}

function InstallDialog({ host, onClose }: { host: Host; onClose: () => void }) {
  const [preset, setPreset] = useState<Preset>("managed");
  const [key, setKey] = useState("");
  const install = useMutation({
    mutationFn: () => unwrap(api.POST("/hosts/{id}/agent/install", { params: { path: { id: host.id } }, body: { preset, release_key: key.trim() || undefined } })),
  });
  return (
    <Dialog title={`Install a Shepherd (DROOL agent) on ${host.name}`} onClose={onClose} wide>
      <div className="space-y-4">
        <p className="text-sm">DROOL copies the agent over this host's pinned SSH connection, writes its capability ceiling
          (/etc/drool/agent.yaml), enrolls it with a single-use token, and starts the drool-agent service. The host then uses the
          agent, with SSH as fallback.</p>
        <ShepherdEndpointLine />
        <div>
          <div className="mb-2 text-sm font-medium">What may DROOL do through the agent? <span className="font-normal text-muted">(an existing agent.yaml on the host is kept)</span></div>
          <PresetPicker value={preset} onChange={(p) => setPreset(p as Preset)} />
        </div>
        <Field label="Also trust a release-signing key (optional)" hint="Only for agent builds you sign yourself (drool release keygen). Anyone with that key's private half can install code that runs as root on this host.">
          <Input className="font-mono text-xs" value={key} onChange={(e) => setKey(e.target.value)} placeholder="drool-release-ed25519 AAAA… my builds" />
        </Field>
        {install.error && <Alert>{errorMessage(install.error)}</Alert>}
        {install.data && (
          <>
            <Alert tone={install.data.ok ? "ok" : "danger"}>{install.data.ok ? "The agent is installed and connected." : install.data.error}</Alert>
            <pre className="max-h-60 overflow-auto whitespace-pre-wrap rounded-ui bg-surface-2 p-2 font-mono text-xs">{install.data.output}</pre>
          </>
        )}
        <div className="flex justify-end gap-2">
          <Button onClick={onClose}>{install.data?.ok ? "Done" : "Cancel"}</Button>
          {!install.data?.ok && (
            <Button variant="primary" disabled={install.isPending} onClick={() => install.mutate()}>
              {install.isPending ? "Installing… (up to a few minutes)" : `Install with the ${preset} preset`}
            </Button>
          )}
        </div>
      </div>
    </Dialog>
  );
}

export function ManualInstallDialog({ host, onClose, groupId }: { host?: Host; onClose: () => void; groupId?: string }) {
  const [preset, setPreset] = useState<Preset>("managed");
  const token = useMutation({
    mutationFn: () => unwrap(api.POST("/agents/tokens", { body: { host_id: host?.id, group_id: groupId, preset, description: host ? `manual install on ${host.name}` : "manual install" } })),
  });
  const t = token.data;
  return (
    <Dialog title={host ? `Install a Shepherd on ${host.name} manually` : "Add a host with a Shepherd (DROOL agent)"} onClose={onClose} wide>
      <div className="space-y-4">
        {!t ? (
          <>
            <p className="text-sm">Creates a single-use enrollment token (valid for one hour) and a one-line installer to run as root on the host.
              The installer checks the download against the controller's pinned key and enrolls the agent by the DROOL Agent Root CA in the token,
              so it doesn't depend on browser certificate trust.</p>
            <ShepherdEndpointLine />
            <PresetPicker value={preset} onChange={(p) => setPreset(p as Preset)} />
            {token.error && <Alert>{errorMessage(token.error)}</Alert>}
            <div className="flex justify-end gap-2">
              <Button onClick={onClose}>Cancel</Button>
              <Button variant="primary" disabled={token.isPending} onClick={() => token.mutate()}>Create token and installer</Button>
            </div>
          </>
        ) : (
          <>
            <p className="text-sm">Run this on the host as root (shown only once; it expires {formatDateTime(t.expires_at)}):</p>
            <CodeBlock text={t.install_command} />
            <details className="text-xs text-muted">
              <summary className="cursor-pointer">Details</summary>
              <p className="mt-1">Agent endpoint: <span className="font-mono">{t.endpoints.join(", ")}</span>. The host must reach it.</p>
              <p>Agent Root CA: <span className="font-mono">{t.root_fingerprint}</span></p>
              {t.pinned_web_key && <p>Download pinned to the web key <span className="font-mono">{t.pinned_web_key}</span></p>}
              <p className="mt-1">Without the installer: copy drool-agent to the host and run <span className="font-mono">drool-agent install --preset {preset} --token-file -</span> with the token on standard input.</p>
              <CodeBlock text={t.token} />
            </details>
            <div className="flex justify-end"><Button onClick={onClose}>Done</Button></div>
          </>
        )}
      </div>
    </Dialog>
  );
}

const compatLabels: Record<Schemas["AgentCompatibility"]["status"], [string, "ok" | "warn" | "danger" | "neutral" | "accent"]> = {
  compatible: ["compatible", "ok"],
  update_recommended: ["update available", "accent"],
  reduced: ["reduced capabilities", "warn"],
  update_required: ["update required", "danger"],
  incompatible: ["incompatible", "danger"],
  trust_failure: ["identity / trust problem", "danger"],
};

/** A Shepherd's compatibility with this controller (M16). */
export function CompatibilityBadge({ c }: { c: Schemas["AgentCompatibility"] }) {
  const [label, tone] = compatLabels[c.status];
  return <Badge tone={tone}>{label}</Badge>;
}

/** Removing a host's Shepherd (dogfooding correction batch): the Shepherd
 * removes itself when it can (no SSH needed), else DROOL removes it over
 * SSH; with neither, it can only be forgotten after a removal by hand. The
 * result says what DROOL could confirm: "initiated" when it couldn't look,
 * "verified" only when SSH showed nothing is left. */
function UninstallDialog({ host, onClose }: { host: Host; onClose: () => void }) {
  const plan = useQuery({
    queryKey: ["hosts", host.id, "agent-uninstall-plan"],
    queryFn: () => unwrap(api.POST("/hosts/{id}/agent/uninstall/plan", { params: { path: { id: host.id } } })),
    gcTime: 0,
    retry: false,
  });
  const run = useMutation({
    mutationFn: (method?: "shepherd" | "ssh" | "forget") => unwrap(api.POST("/hosts/{id}/agent/uninstall", { params: { path: { id: host.id } }, body: method ? { method } : {} })),
  });
  const p = plan.data;
  const res = run.data;
  const tone = res?.outcome === "uninstalled" || res?.outcome === "nothing" ? "ok" : res?.outcome === "initiated" || res?.outcome === "partial" || res?.outcome === "forgotten" ? "warn" : "danger";
  const outcomeTitle: Record<string, string> = {
    uninstalled: "Verified removed", initiated: "Self-removal initiated", partial: "Partly removed", unknown: "Outcome unknown",
    refused: "Refused by the Shepherd", forgotten: "Forgotten in DROOL", nothing: "Nothing to remove",
  };
  const self = p?.method === "shepherd";
  return (
    <Dialog title={`Uninstall the Shepherd from ${host.name}?`} onClose={onClose} wide>
      <div className="space-y-4 text-sm" data-testid="uninstall-shepherd">
        {plan.isPending && <p className="text-muted">Checking how DROOL can reach the Shepherd on {host.name}…</p>}
        {plan.error && <Alert>{errorMessage(plan.error)}</Alert>}
        {p && !res && !p.can_uninstall && (
          <>
            <Alert tone={p.refusal_code === "nothing_to_remove" ? "ok" : "warn"}>{p.refusal}</Alert>
            {p.refusal_code === "no_path" && p.identity && p.identity.status !== "revoked" && (
              <p className="text-muted">
                After removing it by hand (user guide: Removing a Host, “By hand”), <strong>Forget the Shepherd</strong> revokes its identity in DROOL. Nothing on
                {" "}{host.name} changes.
              </p>
            )}
          </>
        )}
        {p && !res && p.can_uninstall && (
          <>
            <section data-testid="uninstall-how">
              <h3 className="mb-1 font-medium">{self ? "The Shepherd removes itself" : `DROOL removes it over SSH (as root, ${p.sign_in})`}</h3>
              <p className="mb-2 text-muted">
                {self
                  ? "DROOL asks it over its own connection: it starts a helper that outlives it, which stops the Shepherd and removes:"
                  : `The Shepherd can't be asked (${p.shepherd.reason}). Over SSH, DROOL stops it${p.systemd === "running" ? " and disables its service" : " and the supervisor that restarts it"}${p.processes > 0 ? ` (${p.processes} process${p.processes === 1 ? "" : "es"} running now)` : ""}, and removes:`}
              </p>
              <ul className="list-disc space-y-1 pl-5">
                {p.items.map((it) => (
                  <li key={it.path} className={p.checked && !it.present ? "text-muted" : ""}>
                    {it.what}: <code className="font-mono text-xs">{it.path}</code>{p.checked && !it.present ? " (not there)" : ""}
                  </li>
                ))}
              </ul>
              {self && (
                <p className="mt-2 text-muted">
                  {p.ssh.available
                    ? "Afterwards DROOL checks over SSH that nothing of it is left."
                    : "DROOL can't look at the host afterwards (no working SSH): it will report the removal as initiated, not verified."}
                </p>
              )}
            </section>
            <section>
              <h3 className="mb-1 font-medium">In DROOL</h3>
              <ul className="list-disc space-y-1 pl-5">{p.controller.map((c) => <li key={c}>{c}</li>)}</ul>
            </section>
            <p className="text-muted">To use a Shepherd again later, install one as before. Files DROOL deployed, its firewall rules and DROOL&apos;s own SSH
              account stay as they are (removing the host can clean up the account).</p>
          </>
        )}
        {run.error && <Alert>{errorMessage(run.error)}</Alert>}
        {res && (
          <>
            <Alert tone={tone}><strong>{outcomeTitle[res.outcome] ?? res.outcome}.</strong> {res.message}</Alert>
            {res.removed.length > 0 && <p>Removed{res.verified ? " (checked over SSH)" : ""}: <span className="font-mono text-xs">{res.removed.join(", ")}</span></p>}
            {res.remaining.length > 0 && <p className="text-warn">Still there: <span className="font-mono text-xs">{res.remaining.join(", ")}</span></p>}
            {res.output && (
              <details><summary className="cursor-pointer text-muted">What the host reported</summary>
                <pre className="mt-1 max-h-48 overflow-auto whitespace-pre-wrap rounded-ui bg-surface-2 p-2 font-mono text-xs">{res.output}</pre>
              </details>
            )}
          </>
        )}
        <div className="flex flex-wrap justify-end gap-2">
          <Button onClick={onClose}>{res ? "Done" : "Cancel"}</Button>
          {!res && p?.can_uninstall && (
            <Button variant="danger" disabled={run.isPending} onClick={() => run.mutate(undefined)}>
              {run.isPending ? (self ? "Removing… (up to a minute)" : "Uninstalling… (up to a minute)") : "Uninstall Shepherd"}
            </Button>
          )}
          {!res && p && !p.can_uninstall && p.refusal_code === "no_path" && p.identity && p.identity.status !== "revoked" && (
            <Button variant="danger" disabled={run.isPending} onClick={() => run.mutate("forget")}>Forget the Shepherd</Button>
          )}
          {res && (res.outcome === "partial" || res.outcome === "unknown") && (
            <Button variant="danger" disabled={run.isPending} onClick={() => { run.reset(); void plan.refetch(); }}>Check again</Button>
          )}
        </div>
      </div>
    </Dialog>
  );
}

function RevokeDialog({ identityId, hostName, onClose }: { identityId: string; hostName: string; onClose: () => void }) {
  const [reason, setReason] = useState("");
  const [understood, setUnderstood] = useState(false);
  const revoke = useMutation({
    mutationFn: () => unwrap(api.POST("/agents/{id}/revoke", { params: { path: { id: identityId } }, body: { reason, leave_unmanaged: understood } })),
    onSuccess: onClose,
  });
  // The Shepherd is the host's only management path (no SSH): the server
  // asks for that to be acknowledged (M16).
  const lastPath = revoke.error instanceof ApiError && revoke.error.problem?.code === "last_management_path";
  return (
    <Dialog title={`Revoke the Shepherd identity of ${hostName}?`} onClose={onClose}>
      <div className="space-y-3 text-sm">
        <p>The agent is disconnected now and can never connect with this identity again; its certificates stop working. DROOL won't manage
          the host until the agent is re-enrolled or you choose SSH management. Use this for a lost, stolen or compromised host.</p>
        <Field label="Reason (recorded in the audit log)"><Input value={reason} onChange={(e) => setReason(e.target.value)} autoFocus /></Field>
        {lastPath ? (
          <Alert tone="warn">
            <p className="mb-2">{errorMessage(revoke.error)}</p>
            <Checkbox checked={understood} onChange={setUnderstood} label={`I understand: DROOL can't manage ${hostName} until its Shepherd is enrolled again on the host`} />
          </Alert>
        ) : revoke.error && <Alert>{errorMessage(revoke.error)}</Alert>}
        <div className="flex justify-end gap-2">
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="danger" disabled={!reason.trim() || revoke.isPending || (lastPath && !understood)} onClick={() => revoke.mutate()}>Revoke</Button>
        </div>
      </div>
    </Dialog>
  );
}

function CapsDialog({ hostId, ceiling, requested, onClose }: { hostId: string; ceiling: AgentCaps; requested: AgentCaps | null; onClose: () => void }) {
  const [c, setC] = useState<AgentCaps>(() => structuredClone(requested ?? ceiling));
  const [paths, setPaths] = useState((requested ?? ceiling).files.allowed_paths?.join("\n") ?? "");
  const [services, setServices] = useState((requested ?? ceiling).services.allowed?.join(" ") ?? "");
  const save = useMutation({
    mutationFn: (caps: AgentCaps | null) => unwrap(api.PUT("/hosts/{id}/agent/caps", { params: { path: { id: hostId } }, body: { caps } })),
    onSuccess: onClose,
  });
  const body = (): AgentCaps => ({
    ...c,
    files: { ...c.files, allowed_paths: paths.split(/\s+/).filter(Boolean) },
    services: { ...c.services, allowed: services.split(/[\s,]+/).filter(Boolean) },
  });
  const toggle = (k: "monitoring" | "shell" | "reboot") => (v: boolean) => setC({ ...c, [k]: v });
  return (
    <Dialog title="Narrow what DROOL may do" onClose={onClose} wide>
      <div className="space-y-3 text-sm">
        <p className="text-muted">Only capabilities the host's ceiling allows can be enabled; the agent intersects whatever you choose with its ceiling.</p>
        <div className="grid gap-2 md:grid-cols-2">
          <Checkbox checked={c.monitoring} onChange={toggle("monitoring")} label="Monitoring" disabled={!ceiling.monitoring} />
          <Checkbox checked={c.shell} onChange={toggle("shell")} label="Shell (commands and scripts)" disabled={!ceiling.shell} />
          <Checkbox checked={c.reboot} onChange={toggle("reboot")} label="Reboot" disabled={!ceiling.reboot} />
          <Checkbox checked={c.packages.enabled} onChange={(v) => setC({ ...c, packages: { ...c.packages, enabled: v } })} label="Packages" disabled={!ceiling.packages.enabled} />
          <Checkbox checked={c.services.enabled} onChange={(v) => setC({ ...c, services: { ...c.services, enabled: v } })} label="Services" disabled={!ceiling.services.enabled} />
          <Checkbox checked={c.files.enabled} onChange={(v) => setC({ ...c, files: { ...c.files, enabled: v } })} label="Files" disabled={!ceiling.files.enabled} />
        </div>
        {c.services.enabled && <Field label="Only these services (empty: as the ceiling allows)"><Input value={services} onChange={(e) => setServices(e.target.value)} placeholder="nginx myapp" /></Field>}
        {c.files.enabled && (
          <Field label="Only these paths (one per line; empty: as the ceiling allows)">
            <textarea className="h-20 rounded-ui border border-line bg-surface p-2 font-mono text-xs" value={paths} onChange={(e) => setPaths(e.target.value)} placeholder="/etc/nginx" />
          </Field>
        )}
        {save.error && <Alert>{errorMessage(save.error)}</Alert>}
        <div className="flex justify-between gap-2">
          <Button onClick={() => save.mutate(null)} disabled={save.isPending}>Remove narrowing</Button>
          <div className="flex gap-2">
            <Button onClick={onClose}>Cancel</Button>
            <Button variant="primary" disabled={save.isPending} onClick={() => save.mutate(body())}>Apply</Button>
          </div>
        </div>
      </div>
    </Dialog>
  );
}

export type { Schemas };
