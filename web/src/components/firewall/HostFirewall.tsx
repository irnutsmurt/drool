import { useCallback, useEffect, useRef, useState } from "react";
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
import { modeInfo, PolicyEditor, statusLabels, TxList } from "./parts";
import { ApplyDialog } from "./ApplyDialog";
import { applyBlocked, needsApply, PolicyStatus, ResolvedPolicy } from "./HostPolicy";
import { InstallNftables } from "./InstallNftables";
import { CollectionActions, CollectionDetail, HostConnections } from "@/components/network/HostNetwork";
import { nk, useHostNetwork } from "@/components/network/queries";
import { plainLink } from "@/components/ui/links";
import { LoadError, Loading } from "@/components/ui/States";
import { fk, useFwHost, useFwSuggestions, useFwTransactions, type FwPlan, type FwSuggestion } from "./queries";
import { TabPanel, Tabs } from "@/components/ui/Tabs";

/** A host's firewall (DESIGN §15–19), on its own page: first what the
 * policy means for this host — saved, applied and observed, each labelled,
 * with the one next action (review and apply) — then the policy as
 * resolved for it, its mode and environment, and observed connections,
 * transactions and the generated rules. */
export function HostFirewall({ host }: { host: Host }) {
  const plan = useFwHost(host.id);
  const p = plan.data;
  const [tab, setTab] = useState<"observed" | "history" | "advanced">("observed");
  // "enforce": the review opens with Enforce chosen (it was chosen under
  // Mode and environment); otherwise a host not in Enforce keeps its mode
  // unless Enforce is chosen in the review.
  const [dialog, setDialog] = useState<"apply" | "enforce" | "remove" | null>(null);
  // When what the host runs (or its mode) changes, e.g. an apply committed,
  // the observations are judged against it anew: ask for them again rather
  // than show the old verdicts for up to their refresh interval.
  const qc = useQueryClient();
  const state = p ? [p.config.mode, p.applied.enforced, p.applied.revision, p.applied.drift ?? "", p.applied.rules_changed].join("|") : "";
  const seen = useRef(state);
  useEffect(() => {
    if (seen.current && state && seen.current !== state) void qc.invalidateQueries({ queryKey: nk.all });
    seen.current = state;
  }, [state, qc]);
  if (plan.error) return <LoadError what="this host's firewall" error={plan.error} />;
  if (!p) return <Loading what="this host's firewall" />;
  const showObserved = () => {
    setTab("observed");
    const t = document.getElementById("hostfw-tab-observed");
    t?.scrollIntoView({ block: "start" });
    t?.focus();
  };
  return (
    <div className="grid gap-4">
      <PolicyStatus p={p} onShowObserved={showObserved} onApply={() => setDialog("apply")} onRemove={() => setDialog("remove")} />
      <ResolvedPolicy p={p} hostName={host.name} />
      <Card title="Mode and environment">
        <div className="grid gap-4 lg:grid-cols-2">
          {/* A new form when the saved settings change underneath (an apply
              switched the mode): never a stale choice offered for saving. */}
          <ModeForm key={`${p.config.mode}|${p.config.outbound ?? ""}|${p.config.rollback_timeout_s ?? 0}`} p={p} onReview={() => setDialog("enforce")} />
          <Environment p={p} hostName={host.name} />
        </div>
      </Card>
      <Card>
        <Tabs label="Firewall details" id="hostfw" className="-mt-1 overflow-x-auto" current={tab} onSelect={setTab}
          tabs={[{ key: "observed", label: "Observed connections" }, { key: "history", label: "Transactions" }, { key: "advanced", label: "Generated nftables" }]} />
        <TabPanel id="hostfw" current={tab} className="pt-3">
          {tab === "observed" && <Observed p={p} />}
          {tab === "history" && <History hostId={host.id} />}
          {tab === "advanced" && (
            <div className="space-y-2 text-sm">
              <p className="text-muted">Diagnostics only: this is what DROOL would load into its own table <span className="font-mono">inet drool</span> from the saved policy.
                DROOL never touches other tables and never runs <span className="font-mono">nft flush ruleset</span>.</p>
              <CodeBlock text={p.rendered.full} />
              <p className="text-xs text-muted">rules {p.rendered.rules_hash.slice(0, 16)} · sets {p.rendered.sets_hash.slice(0, 16)}</p>
            </div>
          )}
        </TabPanel>
      </Card>
      {dialog && <ApplyDialog hostIds={[p.host_id]} names={[host.name]} remove={dialog === "remove"}
        initialModes={dialog === "enforce" ? { [p.host_id]: "enforce" } : undefined} onClose={() => setDialog(null)} />}
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

function ModeForm({ p, onReview }: { p: FwPlan; onReview: () => void }) {
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
  const onlyMode = outbound === (p.config.outbound ?? "allow") && (timeout || "0") === String(p.config.rollback_timeout_s || 0);
  const dirty = mode !== p.config.mode || !onlyMode;
  // Choosing Enforce changes nothing on the host: say what does, and offer
  // it — the review, with Enforce chosen, switches the mode as part of
  // applying (set_enforce_hosts).
  const toEnforce = can("firewall.write") && dirty && mode === "enforce" && p.config.mode !== "enforce";
  const reviewInstead = toEnforce && onlyMode && needsApply(p) && !applyBlocked(p);
  return (
    <div className="space-y-2 text-sm">
      <Field label="Mode" hint={modeInfo[mode]?.text}>
        <Select value={mode} disabled={!can("firewall.write")} onChange={(e) => setMode(e.target.value as FwPlan["config"]["mode"])}>
          {Object.entries(modeInfo).map(([k, v]) => <option key={k} value={k}>{v.label}</option>)}
        </Select>
      </Field>
      <p className="text-xs text-muted" data-testid="fw-in-force">{inForce(p)}</p>
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
      {toEnforce && (
        <p className="rounded-ui border border-line px-3 py-2" data-testid="fw-enforce-next">
          <strong>Selecting Enforce doesn&apos;t change anything on the host by itself:</strong>{" "}
          {p.applied.enforced ? "the saved changes take effect when the policy is applied." : "DROOL's rules take effect when the policy is applied."}{" "}
          {reviewInstead ? "Review and apply policy… shows what changes, then switches this host to Enforce and applies the policy in one confirmed step."
            : "Save the mode, then review and apply the policy (Policy on this host, above)."}
        </p>
      )}
      {can("firewall.write") && dirty && mode !== "enforce" && p.applied.enforced && (
        <p className="text-xs text-warn" data-testid="fw-mode-keeps">
          <span aria-hidden="true">⚠ </span>Changing the mode doesn&apos;t remove DROOL&apos;s rules: revision {p.applied.revision} stays in force on the host until you remove it
          (Remove DROOL policy…, above).
        </p>
      )}
      {reviewInstead ? (
        <div className="flex flex-wrap gap-2">
          <Button size="sm" variant="primary" onClick={onReview}>Review and apply policy…</Button>
          <Button size="sm" disabled={save.isPending} onClick={() => save.mutate()}>Save mode only</Button>
        </div>
      ) : can("firewall.write") && dirty && <Button size="sm" variant="primary" disabled={save.isPending} onClick={() => save.mutate()}>Save</Button>}
    </div>
  );
}

/** What DROOL's firewall actually does on the host, beside the mode it is
 * set to (DESIGN-STANDARD §8): the setting is a wish until a policy is
 * applied, and an applied revision stays in force whatever the mode. */
function inForce(p: FwPlan): string {
  const { applied } = p;
  if (applied.enforced && applied.drift !== "missing") {
    return `In force on the host: DROOL's policy, revision ${applied.revision}${applied.rules_changed ? ", with saved changes not applied yet" : ""}.`;
  }
  return `In force on the host: nothing of DROOL's${applied.drift === "missing" ? " (its rules are missing from the host)" : ""}. The mode is what DROOL is set to do${
    p.config.mode === "enforce" ? "; Enforce takes effect when the policy is applied" : ""}.`;
}

function Environment({ p, hostName }: { p: FwPlan; hostName: string }) {
  const { can } = useAuth();
  const qc = useQueryClient();
  // Check only inspects the host (read-only); installing is a separate,
  // confirmed step (InstallNftables).
  const detect = useMutation({
    mutationFn: () => unwrap(api.POST("/firewall/hosts/{id}/detect", { params: { path: { id: p.host_id } } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: fk.host(p.host_id) }),
  });
  const { mutate: check } = detect;
  const recheck = useCallback(() => check(), [check]);
  const [nftJob, setNftJob] = useState<string>();
  const el = p.eligibility;
  const enforce = p.config.mode === "enforce";
  const nftMissing = p.environment?.nft === "";
  const reasons = (el?.reasons ?? []).map((r) => <span key={r} className="block">• {r}</span>);
  const install = (nftMissing || nftJob) && (
    <InstallNftables hostId={p.host_id} hostName={hostName} mode={p.config.mode} jobId={nftJob} onStarted={setNftJob}
      onInstalled={recheck} checking={detect.isPending} checkFailed={detect.isError} />
  );
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
            : enforce
              // Enforce is set but can't take effect: a problem to resolve.
              ? <Alert><strong>Enforce is set, but DROOL can&apos;t enforce on this host:</strong> nothing is filtered until this is resolved.
                {reasons}
                <span className="mt-1 block text-xs">Monitoring, simulation and explanation still work.</span></Alert>
              // In the other modes enforcement isn't asked for: information.
              : <div className="rounded-ui border border-line px-3 py-2" data-testid="fw-env-info">
                <p><strong>Enforcement isn&apos;t available here yet.</strong>{" "}
                  {p.config.mode === "off" ? "That matters only if you switch to Enforce."
                    : `${modeInfo[p.config.mode]?.label ?? p.config.mode} mode doesn't need it: monitoring, simulation and explanation work. Resolve it before you switch to Enforce.`}</p>
                <div className="text-muted">{reasons}</div>
              </div>}
          {install}
          {(el.warnings ?? []).map((w) => <p key={w} className="text-xs text-warn">{w}</p>)}
          {(el.detected ?? []).length > 0 && <p className="text-xs text-muted">Detected: {(el.detected ?? []).join(" · ")}</p>}
          {p.environment_checked_at && <p className="text-xs text-muted">checked {formatAgo(p.environment_checked_at)}</p>}
        </>
      )}
    </div>
  );
}

/** Observed connections on this host, with how the saved policy treats
 * each of them (FlowTable, the same list as the host's Network page), so
 * a person can look at one and start a rule from it without leaving the
 * firewall page. Observing is never proof of what the firewall did. */
function Observed({ p }: { p: FwPlan }) {
  const { can } = useAuth();
  const net = useHostNetwork(p.host_id, can("network.read"));
  const sug = useFwSuggestions(p.host_id, can("network.read") && (p.config.mode === "guided" || p.config.mode === "enforce"));
  const [approve, setApprove] = useState<FwSuggestion | null>(null);
  if (!can("network.read")) {
    return <p className="text-sm text-muted" data-testid="fw-observed-denied">Viewing observed connections needs the network.read permission (operators, auditors and administrators). The policy above doesn&apos;t depend on it.</p>;
  }
  const hn = net.data;
  return (
    <div className="space-y-3 text-sm" data-testid="fw-observed-tab">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <p className="min-w-0 flex-1 basis-72 text-muted">
          What DROOL saw on this host, with what the saved policy says about each connection. Seeing a connection isn&apos;t proof that the firewall allowed it.
          Select one for its details; <strong className="font-medium text-fg">Create policy from observed connection…</strong> there adds a draft rule in the Firewall Designer,
          in any mode: nothing is saved or applied until you do.
        </p>
        {hn && <CollectionActions hostId={p.host_id} hn={hn} />}
      </div>
      {net.error && <LoadError what="this host's observed connections" error={net.error} />}
      {!hn && !net.error && <Loading what="this host's observed connections" />}
      {hn && <CollectionDetail hn={hn} />}
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
      {!net.error && <HostConnections hostId={p.host_id} hn={hn} firewallLink={false} />}
      <p className="text-xs text-muted">
        More filters, other hosts and the map are in <Link className={plainLink} to={`/network?host=${encodeURIComponent(p.host_id)}`}>Network Activity</Link>.
      </p>
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
  return <TxList txs={txs.data?.items ?? []} q={txs} />;
}
