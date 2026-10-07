import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import type { Host } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { CodeBlock } from "@/components/ui/CodeBlock";
import { cx } from "@/components/ui/cx";
import { Field, Input, Select } from "@/components/ui/Field";
import { Table } from "@/components/ui/Table";
import { errorMessage, formatAgo, formatDateTime } from "@/lib/format";
import { ApplyDialog, ExplainTable, modeInfo, PolicyEditor, statusLabels, TxList } from "./parts";
import { fk, useFwHost, useFwObservations, useFwSuggestions, useFwTransactions, type FwPlan, type FwSuggestion } from "./queries";

/** The host's firewall (DESIGN §15–19): mode, environment, what is
 *  enforced and why, observations, and transactions. */
export function FirewallCard({ host }: { host: Host }) {
  const plan = useFwHost(host.id);
  const p = plan.data;
  const [tab, setTab] = useState<"explain" | "observed" | "history" | "advanced">("explain");
  if (plan.error) return <Card title="Firewall"><Alert>{errorMessage(plan.error)}</Alert></Card>;
  if (!p) return <Card title="Firewall"><p className="text-sm text-muted">Loading…</p></Card>;
  const st = statusLabels[p.summary.status];
  return (
    <Card title="Firewall" actions={<span className="flex gap-2"><Badge tone={st.tone}>{st.text}</Badge></span>}>
      <div className="grid gap-4 lg:grid-cols-2">
        <ModeForm p={p} />
        <Environment p={p} />
      </div>
      <Status p={p} host={host} />
      <div className="mt-4 flex gap-1 border-b border-line">
        {([["explain", "Effective policy"], ["observed", "Observed connections"], ["history", "Transactions"], ["advanced", "Generated nftables"]] as const).map(([id, label]) => (
          <button key={id} onClick={() => setTab(id)}
            className={cx("-mb-px border-b-2 px-3 py-2 text-sm", tab === id ? "border-accent font-medium" : "border-transparent text-muted hover:text-fg")}>{label}</button>
        ))}
      </div>
      <div className="pt-3">
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
      </div>
    </Card>
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
      <div className="grid grid-cols-2 gap-2">
        <Field label="Outbound traffic" hint={outbound === "restrict" ? "Only what policies allow (plus the management path)." : "Not filtered (denies still apply)."}>
          <Select value={outbound} disabled={!can("firewall.write")} onChange={(e) => setOutbound(e.target.value as "allow" | "restrict")}>
            <option value="allow">allow</option>
            <option value="restrict">restrict</option>
          </Select>
        </Field>
        <Field label="Rollback timeout (s)" hint={`now ${p.rollback_timeout_s}s (${p.rollback_timeout_from})`}>
          <Input type="number" min={30} value={timeout} disabled={!can("firewall.write")} placeholder="inherit" onChange={(e) => setTimeout_(e.target.value)} />
        </Field>
      </div>
      <p className="text-xs text-muted">Learning: {p.learning === "continuous" ? "continuous (agent)" : p.learning === "snapshot" ? "connection snapshots only (SSH host)" : "off"}</p>
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
    <div className="mt-4 space-y-2 border-t border-line pt-3 text-sm">
      <dl className="grid grid-cols-[11rem_1fr] gap-y-1">
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
  const qc = useQueryClient();
  const obs = useFwObservations(p.host_id);
  const sug = useFwSuggestions(p.host_id, p.config.mode === "guided" || p.config.mode === "enforce");
  const snap = useMutation({
    mutationFn: () => unwrap(api.POST("/firewall/hosts/{id}/snapshot", { params: { path: { id: p.host_id } } })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: fk.observations(p.host_id) });
      void qc.invalidateQueries({ queryKey: fk.suggestions(p.host_id) });
    },
  });
  const [approve, setApprove] = useState<FwSuggestion | null>(null);
  return (
    <div className="space-y-3 text-sm">
      <div className="flex items-center justify-between">
        <p className="text-muted">
          {p.learning === "continuous" ? "The agent records connections continuously (aggregated: who talks to whom on which port)."
            : p.learning === "snapshot" ? "SSH hosts don't learn continuously: take a snapshot of the current connections."
              : p.transport === "agent" ? "Mode is off: the agent isn't recording connections. Turn on Monitor, Guided or Enforce for continuous learning, or take a one-off snapshot."
                : "Take a snapshot of the current connections (SSH hosts never learn continuously)."}
        </p>
        {can("firewall.write") && <Button size="sm" disabled={snap.isPending} onClick={() => snap.mutate()}>Take snapshot now</Button>}
      </div>
      {snap.error && <Alert>{errorMessage(snap.error)}</Alert>}
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
      <Table
        rows={obs.data?.items ?? []}
        rowKey={(o) => o.direction + o.protocol + o.port + o.peer}
        empty="No connections observed yet."
        columns={[
          { header: "", cell: (o) => (o.classified ? <Badge tone="ok">covered</Badge> : <Badge tone="warn">unclassified</Badge>) },
          { header: "Direction", cell: (o) => (o.direction === "in" ? "inbound" : "outbound") },
          { header: "Peer", cell: (o) => o.peer_host ? <span>{o.peer_host} <span className="text-xs text-muted">{o.peer}</span></span> : o.peer },
          { header: "Service", cell: (o) => `${o.protocol.toUpperCase()} ${o.port}${o.service ? ` (${o.service})` : ""}` },
          { header: "Connections", cell: (o) => o.count.toLocaleString() },
          { header: "Seen", cell: (o) => <span title={formatDateTime(o.last_seen)}>{formatAgo(o.last_seen)} <span className="text-xs text-muted">{o.source}</span></span> },
          { header: "Policy", cell: (o) => <span className={o.verdict === "drop" ? "text-danger" : ""}>{o.verdict === "drop" ? "would be blocked" : "allowed"}: {o.reason}</span> },
        ]}
      />
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
