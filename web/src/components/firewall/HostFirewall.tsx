import { useState } from "react";
import { Link } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import type { Host } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { CodeBlock } from "@/components/ui/CodeBlock";
import { Field, Input, Select } from "@/components/ui/Field";
import { errorMessage, formatAgo } from "@/lib/format";
import { ApplyDialog, ExplainTable, modeInfo, PolicyEditor, statusLabels, TxList } from "./parts";
import { fk, useFwHost, useFwSuggestions, useFwTransactions, type FwPlan, type FwSuggestion } from "./queries";
import { TabPanel, Tabs } from "@/components/ui/Tabs";

/** A host's firewall (DESIGN §15–19), on its own page: mode,
 *  environment, what is enforced and why, observations, and transactions. */
export function HostFirewall({ host }: { host: Host }) {
  const plan = useFwHost(host.id);
  const p = plan.data;
  const [tab, setTab] = useState<"explain" | "observed" | "history" | "advanced">("explain");
  if (plan.error) return <Alert>{errorMessage(plan.error)}</Alert>;
  if (!p) return <p className="text-sm text-muted">Loading…</p>;
  return (
    <div className="grid gap-4">
      <Card title="Mode and environment">
        <div className="grid gap-4 lg:grid-cols-2">
          <ModeForm p={p} />
          <Environment p={p} />
        </div>
      </Card>
      <Card title="Enforcement">
        <Status p={p} host={host} />
      </Card>
      <Card>
        <Tabs label="Firewall details" id="hostfw" className="-mt-1 overflow-x-auto" current={tab} onSelect={setTab}
          tabs={[{ key: "explain", label: "Effective policy" }, { key: "observed", label: "Observed connections" }, { key: "history", label: "Transactions" }, { key: "advanced", label: "Generated nftables" }]} />
        <TabPanel id="hostfw" current={tab} className="pt-3">
          {tab === "explain" && <ExplainSection p={p} />}
          {tab === "observed" && <Observed p={p} />}
          {tab === "history" && <History hostId={host.id} />}
          {tab === "advanced" && (
            <div className="space-y-2 text-sm">
              <p className="text-muted">Diagnostics only: this is what DROOL would load into its own table <span className="font-mono">inet drool</span>.
                DROOL never touches other tables and never runs <span className="font-mono">nft flush ruleset</span>.</p>
              <CodeBlock text={p.rendered.full} />
              <p className="text-xs text-muted">rules {p.rendered.rules_hash.slice(0, 16)} · sets {p.rendered.sets_hash.slice(0, 16)}</p>
            </div>
          )}
        </TabPanel>
      </Card>
    </div>
  );
}

/** The host overview's firewall line: mode, enforcement, drift and safety
 * state, with the way to the host's firewall page. */
export function HostFirewallSummary({ hostId }: { hostId: string }) {
  const plan = useFwHost(hostId);
  const p = plan.data;
  if (plan.error) return <p className="text-sm text-danger">Couldn't load the firewall state: {errorMessage(plan.error)}</p>;
  if (!p) return <p className="text-sm text-muted">Loading…</p>;
  const st = statusLabels[p.summary.status];
  const conflicts = p.policy.conflicts ?? [];
  return (
    <div className="space-y-1.5 text-sm">
      <p className="flex flex-wrap items-center gap-2">
        <Badge tone={st.tone}>{st.text}</Badge>
        <span>Mode: <b>{modeInfo[p.config.mode]?.label ?? p.config.mode}</b></span>
        <span className="text-muted">· {p.applied.enforced ? `revision ${p.applied.revision} enforced` : "DROOL isn't filtering this host"}</span>
      </p>
      {p.applied.drift && <p className="text-danger"><span aria-hidden="true">✕ </span>DROOL's rules were changed outside DROOL (drift).</p>}
      {conflicts.length > 0 && <p className="text-danger"><span aria-hidden="true">✕ </span>Applying is refused: a policy would cut DROOL's management path.</p>}
      {p.pending_transaction && <p className="text-warn"><span aria-hidden="true">⚠ </span>Transaction {p.pending_transaction.state}: reboots and network restarts wait for it.</p>}
      {p.eligibility && !p.eligibility.supported && p.config.mode !== "off" && (
        <p className="text-muted">Enforcement unavailable here{(p.eligibility.reasons ?? [])[0] ? `: ${(p.eligibility.reasons ?? [])[0]}` : ""}. Monitoring, simulation and explanation still work.</p>
      )}
      {p.applied.override_mgmt && <p className="text-danger">Applied with the emergency override.</p>}
    </div>
  );
}

function ModeForm({ p }: { p: FwPlan }) {
  const { can } = useAuth();
  const qc = useQueryClient();
  const [mode, setMode] = useState(p.config.mode);
  const [outbound, setOutbound] = useState(p.config.outbound ?? "allow");
  const [timeout, setTimeout_] = useState(String(p.config.rollback_timeout_s || ""));
  const save = useMutation({
    mutationFn: () => unwrap(api.PUT("/firewall/hosts/{id}", { params: { path: { id: p.host_id } },
      body: { mode, outbound: outbound as "allow" | "restrict", rollback_timeout_s: timeout ? Number(timeout) : 0 } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: fk.all }),
  });
  const dirty = mode !== p.config.mode || outbound !== (p.config.outbound ?? "allow") || (timeout || "0") !== String(p.config.rollback_timeout_s || 0);
  return (
    <div className="space-y-2 text-sm">
      <Field label="Mode" hint={modeInfo[mode]?.text}>
        <Select value={mode} disabled={!can("firewall.write")} onChange={(e) => setMode(e.target.value as FwPlan["config"]["mode"])}>
          {Object.entries(modeInfo).map(([k, v]) => <option key={k} value={k}>{v.label}</option>)}
        </Select>
      </Field>
      <Field label="Outbound traffic" hint={outbound === "restrict" ? "Only what policies allow (plus the management path)." : "Not filtered (denies still apply)."}>
        <Select value={outbound} disabled={!can("firewall.write")} onChange={(e) => setOutbound(e.target.value as "allow" | "restrict")}>
          <option value="allow">Allow (recommended)</option>
          <option value="restrict">Restrict to what policies allow</option>
        </Select>
      </Field>
      {/* The safety timer's length: inherited (pack, then the global
          default); a tuning detail, not part of choosing a mode. */}
      <details className="text-sm" open={timeout !== ""}>
        <summary className="cursor-pointer text-muted">Advanced: rollback timeout ({p.rollback_timeout_s} s, {p.rollback_timeout_from})</summary>
        <div className="mt-2 max-w-xs">
          <Field label="Rollback timeout (s)" hint="How long the host waits for DROOL to confirm a change over a new connection before it restores its previous rules. Empty: inherited.">
            <Input type="number" min={30} value={timeout} disabled={!can("firewall.write")} placeholder="inherit" onChange={(e) => setTimeout_(e.target.value)} />
          </Field>
        </div>
      </details>
      <p className="text-xs text-muted">Learning: {p.learning === "continuous" ? "continuous (Shepherd)" : p.learning === "snapshot" ? "connection snapshots only (SSH host)" : "off"}</p>
      {save.error && <Alert>{errorMessage(save.error)}</Alert>}
      {can("firewall.write") && dirty && <Button size="sm" variant="primary" disabled={save.isPending} onClick={() => save.mutate()}>Save</Button>}
    </div>
  );
}

function Environment({ p }: { p: FwPlan }) {
  const { can } = useAuth();
  const qc = useQueryClient();
  const detect = useMutation({
    mutationFn: () => unwrap(api.POST("/firewall/hosts/{id}/detect", { params: { path: { id: p.host_id } } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: fk.host(p.host_id) }),
  });
  const el = p.eligibility;
  return (
    <div className="space-y-2 text-sm">
      <div className="flex items-center justify-between">
        <span className="font-medium">Firewall environment</span>
        {can("firewall.write") && <Button size="sm" disabled={detect.isPending} onClick={() => detect.mutate()}>{detect.isPending ? "Checking…" : "Check"}</Button>}
      </div>
      {detect.error && <Alert>{errorMessage(detect.error)}</Alert>}
      {!el ? <p className="text-muted">Not checked yet.</p> : (
        <>
          {el.supported
            ? <Alert tone="ok">Enforcement is supported here: plain nftables, no other firewall manager.</Alert>
            : <Alert><strong>Firewall enforcement unavailable.</strong> {(el.reasons ?? []).map((r) => <span key={r} className="block">• {r}</span>)}
              <span className="mt-1 block text-xs">Monitoring, simulation and explanation still work.</span></Alert>}
          {(el.warnings ?? []).map((w) => <p key={w} className="text-xs text-warn">{w}</p>)}
          {(el.detected ?? []).length > 0 && <p className="text-xs text-muted">Detected: {(el.detected ?? []).join(" · ")}</p>}
          {p.environment_checked_at && <p className="text-xs text-muted">checked {formatAgo(p.environment_checked_at)}</p>}
        </>
      )}
    </div>
  );
}

function Status({ p, host }: { p: FwPlan; host: Host }) {
  const { can } = useAuth();
  const [dialog, setDialog] = useState<"apply" | "remove" | null>(null);
  const conflicts = p.policy.conflicts ?? [];
  const canApply = can("firewall.write") && p.config.mode === "enforce" && !p.pending_transaction;
  return (
    <div className="space-y-2 text-sm">
      <dl className="grid grid-cols-[7rem_1fr] sm:grid-cols-[11rem_1fr] gap-y-1">
        <dt className="text-muted">Committed on the host</dt>
        <dd>{p.applied.enforced ? `revision ${p.applied.revision}` : "no DROOL policy"}{p.applied.override_mgmt && <span className="ml-2"><Badge tone="danger">applied with emergency override</Badge></span>}</dd>
        {p.applied.enforced && (<>
          <dt className="text-muted">Address sets</dt>
          <dd>generation {p.applied.sets_generation_applied}{p.applied.sets_pending && <span className="ml-2 text-accent">newer addresses are being propagated</span>}
            {p.applied.sets_error && <span className="ml-2 text-danger">last update failed: {p.applied.sets_error}</span>}</dd>
        </>)}
        <dt className="text-muted">Management path</dt>
        <dd>{p.transport === "agent" ? "agent (preferred)" : p.transport}</dd>
      </dl>
      {p.applied.drift && (
        <Alert><strong>Drift:</strong> DROOL's rules on this host {p.applied.drift === "missing" ? "are missing" : "were changed"} outside DROOL
          (for example by <span className="font-mono">nft</span> or a reload of nftables.service). Apply again and choose to replace them.</Alert>
      )}
      {p.pending_transaction && <Alert tone="warn">Transaction {p.pending_transaction.id} is {p.pending_transaction.state}. Reboots and network restarts wait for it.</Alert>}
      {conflicts.length > 0 && (
        <Alert><strong>Refused: a policy would cut DROOL off.</strong>{conflicts.map((c) => <span key={c.message} className="block">• {c.message}</span>)}</Alert>
      )}
      {(p.management_warnings ?? []).map((w) => <p key={w} className="text-xs text-muted">{w}</p>)}
      {(p.policy.warnings ?? []).map((w) => <p key={w} className="text-xs text-warn">{w}</p>)}
      {p.config.mode !== "enforce" && !p.applied.enforced && <p className="text-xs text-muted">Set the mode to Enforce to apply this policy. Until then, Explain shows what it would do.</p>}
      {p.config.mode !== "enforce" && p.applied.enforced && (
        <Alert tone="warn">DROOL's last committed policy (revision {p.applied.revision}) is still enforced on this host. Changing the mode doesn't remove it:
          use <strong>Remove DROOL policy</strong> to stop filtering, or switch back to Enforce to update it.</Alert>
      )}
      <div className="flex flex-wrap gap-2">
        {canApply && (p.applied.rules_changed || !p.applied.enforced || p.applied.drift) && (
          <Button variant="primary" onClick={() => setDialog("apply")}>{p.applied.enforced ? "Apply changes" : "Apply policy"}</Button>
        )}
        {canApply && p.applied.enforced && !p.applied.rules_changed && !p.applied.drift && <Button onClick={() => setDialog("apply")}>Apply again</Button>}
        {can("firewall.write") && p.applied.enforced && !p.pending_transaction && <Button variant="ghost" onClick={() => setDialog("remove")}>Remove DROOL policy</Button>}
      </div>
      {dialog && <ApplyDialog hostIds={[p.host_id]} names={[host.name]} remove={dialog === "remove"}
        changes={p.applied.enforced ? { added: p.changes_added, removed: p.changes_removed } : undefined} onClose={() => setDialog(null)} />}
    </div>
  );
}

function ExplainSection({ p }: { p: FwPlan }) {
  return (
    <div className="space-y-2 text-sm">
      <p className="text-muted">
        {p.config.mode === "enforce" && p.applied.enforced
          ? "What DROOL allows and denies on this host, and why."
          : "What this host's policy would allow and deny once enforced, and why. DROOL doesn't filter this host right now."}
        {" "}Rules are evaluated top to bottom: replies to allowed connections, the management path, denies, then allows.
      </p>
      <ExplainTable rows={p.explain} />
    </div>
  );
}

function Observed({ p }: { p: FwPlan }) {
  const { can } = useAuth();
  const sug = useFwSuggestions(p.host_id, can("network.read") && (p.config.mode === "guided" || p.config.mode === "enforce"));
  const [approve, setApprove] = useState<FwSuggestion | null>(null);
  return (
    <div className="space-y-3 text-sm">
      <p className="text-muted">
        {p.learning === "continuous" ? "The Shepherd records connections continuously."
          : p.learning === "snapshot" ? "SSH hosts are never observed continuously: take a snapshot of the current connections."
            : "Continuous collection is off for this host (Network Activity → Collection and enrichment)."}{" "}
        Observed connections, with how this policy classifies each of them, are on this host&apos;s{" "}
        <Link className="text-accent underline" to={`/hosts/${p.host_id}/network`}>Network Activity</Link> page.
      </p>
      {(sug.data?.items ?? []).length > 0 && (
        <div className="rounded-ui border border-line p-3">
          <div className="mb-1 font-medium">Suggested policies</div>
          <ul className="space-y-1">
            {sug.data!.items.map((s) => (
              <li key={s.title} className="flex items-center justify-between gap-2">
                <span>{s.title} <span className="text-xs text-muted">({s.count} connections)</span></span>
                {can("firewall.write") && <Button size="sm" onClick={() => setApprove(s)}>Review…</Button>}
              </li>
            ))}
          </ul>
        </div>
      )}
      {approve && <ApproveSuggestion s={approve} onClose={() => setApprove(null)} />}
    </div>
  );
}

function ApproveSuggestion({ s, onClose }: { s: FwSuggestion; onClose: () => void }) {
  const qc = useQueryClient();
  const [step, setStep] = useState<"service" | "policy">(s.new_service ? "service" : "policy");
  const mkSvc = useMutation({
    mutationFn: () => unwrap(api.POST("/firewall/services", { body: s.new_service! })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: fk.services });
      setStep("policy");
    },
  });
  if (step === "service") {
    return (
      <div className="rounded-ui border border-accent p-3 text-sm">
        <p>This suggestion needs a new service <strong>{s.new_service!.name}</strong> ({s.new_service!.entries.map((e) => `${e.protocol} ${e.ports}`).join(", ")}).</p>
        {mkSvc.error && <Alert>{errorMessage(mkSvc.error)}</Alert>}
        <div className="mt-2 flex gap-2"><Button size="sm" variant="primary" onClick={() => mkSvc.mutate()}>Create the service</Button><Button size="sm" onClick={onClose}>Cancel</Button></div>
      </div>
    );
  }
  return <PolicyEditor draft={s.policy} onClose={onClose} />;
}

function History({ hostId }: { hostId: string }) {
  const txs = useFwTransactions(hostId);
  return <TxList txs={txs.data?.items ?? []} />;
}
