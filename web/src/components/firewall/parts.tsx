import { useEffect, useState } from "react";
import { Link } from "react-router";
import { useMutation, useQueries, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { useGroups, useHosts } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Checkbox } from "@/components/ui/Checkbox";
import { Dialog } from "@/components/ui/Dialog";
import { Field, Input, Select } from "@/components/ui/Field";
import { Table } from "@/components/ui/Table";
import { errorMessage, formatDateTime } from "@/lib/format";
import { fk, useFwServices, type FwAllowance, type FwHostSummary, type FwPolicy, type FwPolicyInput, type FwSelector, type FwTransaction } from "./queries";
import { emptyText } from "@/components/ui/States";

/** Human labels for selectors. */
export function useSelectorLabel() {
  const hosts = useHosts();
  const groups = useGroups();
  return (s: FwSelector): string => {
    switch (s.kind) {
      case "host":
        return hosts.data?.items.find((h) => h.id === s.id)?.name ?? "host " + s.id;
      case "group":
        return "pack " + (groups.data?.items.find((g) => g.id === s.id)?.name ?? s.id);
      case "internet":
        return "the internet";
      case "internal":
        return "internal networks";
      case "any":
        return "anywhere";
      default:
        return s.cidr ?? "?";
    }
  };
}

export const statusLabels: Record<FwHostSummary["status"], { text: string; tone: "ok" | "warn" | "danger" | "neutral" | "accent" }> = {
  not_enforced: { text: "not enforced", tone: "neutral" },
  in_sync: { text: "in sync", tone: "ok" },
  changes_pending: { text: "changes not applied", tone: "warn" },
  sets_pending: { text: "addresses propagating", tone: "accent" },
  drift: { text: "drift", tone: "danger" },
  pending_transaction: { text: "transaction pending", tone: "warn" },
  conflicts: { text: "blocked: management path", tone: "danger" },
};

export const modeInfo: Record<string, { label: string; text: string }> = {
  off: { label: "Off", text: "DROOL doesn't observe or change this host's firewall." },
  monitor: { label: "Monitor", text: "Observe connections and explain what a policy would do. Nothing is changed." },
  guided: { label: "Guided", text: "Observe, and suggest policies from what is observed for you to approve. Nothing is changed." },
  enforce: { label: "Enforce", text: "Apply the policy with automatic rollback protection — the host reverts a change by itself unless a fresh management connection verifies it — and watch for drift." },
};

export function TxStateBadge({ state }: { state: string }) {
  const tone = state === "committed" ? "ok" : state === "pending" || state === "preparing" ? "warn" : state === "failed" ? "neutral" : "danger";
  return <Badge tone={tone}>{state.replace("_", " ")}</Badge>;
}

/** Explain Host: allowances and denies with their reasons. */
export function ExplainTable({ rows }: { rows: FwAllowance[] }) {
  const [showBase, setShowBase] = useState(false);
  const visible = rows.filter((r) => showBase || r.reason.kind !== "base");
  return (
    <div>
      <div className="mb-2 flex justify-end">
        <Checkbox checked={showBase} onChange={setShowBase} label="Show base rules (replies, loopback, ICMP, DHCP)" />
      </div>
      <Table
        rows={visible}
        rowKey={(r) => r.direction + r.verdict + r.service + r.peer + r.reason.text}
        columns={[
          { header: "Direction", cell: (r) => (r.direction === "in" ? "inbound" : "outbound") },
          { header: "", cell: (r) => <Badge tone={r.verdict === "accept" ? "ok" : "danger"}>{r.verdict === "accept" ? "allow" : "deny"}</Badge> },
          { header: "Traffic", cell: (r) => r.service },
          {
            header: "From / to", cell: (r) => (
              <span>{r.peer}{r.members && r.members.length > 0 && <span className="block text-xs text-muted">{r.members.join(", ")}</span>}</span>
            ),
          },
          {
            header: "Why", cell: (r) => (
              <span className={r.reason.kind === "management" ? "text-accent" : r.reason.kind === "default" ? "text-muted" : ""}>
                {r.reason.kind === "management" && <Badge tone="accent">management path</Badge>} {r.reason.text}
              </span>
            ),
          },
        ]}
      />
    </div>
  );
}

export function TxTimeline({ tx }: { tx: FwTransaction }) {
  return (
    <div className="space-y-2 text-sm">
      <div className="flex flex-wrap items-center gap-2">
        <TxStateBadge state={tx.state} />
        <span className="font-mono text-xs">{tx.id}</span>
        <span className="text-muted">revision {tx.revision} · over {tx.transport || "—"} · rollback after {tx.timeout_s}s · fresh path {tx.measured_ms} ms</span>
        {tx.override_mgmt && <Badge tone="danger">emergency override</Badge>}
        {tx.allow_drift && <Badge tone="warn">replaced drift</Badge>}
        {tx.job_id && <Link className="text-accent underline" to={`/jobs/${tx.job_id}`}>job</Link>}
      </div>
      {tx.detail && <p>{tx.detail}</p>}
      <ol className="space-y-0.5 border-l border-line pl-3 text-xs">
        {tx.steps.map((s, i) => (
          <li key={i}><span className="text-muted">{formatDateTime(s.at)}</span> {s.text}</li>
        ))}
      </ol>
    </div>
  );
}

export function TxList({ txs, q, hostName }: { txs: FwTransaction[]; q: { isLoading: boolean; error: unknown }; hostName?: (id: string) => string }) {
  const [open, setOpen] = useState<string | null>(null);
  return (
    <Table
      rows={txs}
      rowKey={(t) => t.id}
      empty={emptyText("firewall transactions", q, "No firewall transactions yet.")}
      columns={[
        { header: "When", cell: (t) => formatDateTime(t.created_at) },
        ...(hostName ? [{ header: "Host", cell: (t: FwTransaction) => <Link className="underline" to={`/hosts/${t.host_id}`}>{hostName(t.host_id)}</Link> }] : []),
        { header: "State", cell: (t) => <TxStateBadge state={t.state} /> },
        { header: "Kind", cell: (t) => (t.kind === "remove" ? "remove policy" : `revision ${t.revision}`) },
        { header: "Path", cell: (t) => t.transport || "—" },
        {
          header: "Result", cell: (t) => (
            <div>
              <button className="text-left text-accent hover:underline" onClick={() => setOpen(open === t.id ? null : t.id)}>{t.detail || "details"}</button>
              {open === t.id && <div className="mt-2"><TxTimeline tx={t} /></div>}
            </div>
          ),
        },
      ]}
    />
  );
}

// --- policy editor ---

const kinds: { kind: FwSelector["kind"]; label: string }[] = [
  { kind: "group", label: "Pack" },
  { kind: "host", label: "Host" },
  { kind: "internal", label: "Internal networks" },
  { kind: "internet", label: "The internet" },
  { kind: "any", label: "Anywhere / any host" },
  { kind: "cidr", label: "Network (CIDR)" },
];

function SelectorPicker({ value, onChange, label }: { value: FwSelector[]; onChange: (v: FwSelector[]) => void; label: string }) {
  const hosts = useHosts();
  const groups = useGroups();
  const name = useSelectorLabel();
  const [kind, setKind] = useState<FwSelector["kind"]>("group");
  const [id, setId] = useState("");
  const [cidr, setCidr] = useState("");
  const add = () => {
    const s: FwSelector = kind === "cidr" ? { kind, cidr: cidr.trim() } : kind === "host" || kind === "group" ? { kind, id } : { kind };
    if ((kind === "host" || kind === "group") && !id) return;
    if (kind === "cidr" && !cidr.trim()) return;
    onChange([...value, s]);
    setId("");
    setCidr("");
  };
  return (
    <div>
      <div className="mb-1 text-sm font-medium">{label}</div>
      <div className="mb-2 flex flex-wrap gap-1">
        {value.length === 0 && <span className="text-xs text-muted">nothing yet</span>}
        {value.map((s, i) => (
          <span key={i} className="inline-flex items-center gap-1 rounded bg-surface-2 px-2 py-0.5 text-xs">
            {name(s)}
            <button aria-label="remove" className="text-muted hover:text-danger" onClick={() => onChange(value.filter((_, j) => j !== i))}>×</button>
          </span>
        ))}
      </div>
      <div className="flex gap-2">
        <Select value={kind} onChange={(e) => setKind(e.target.value as FwSelector["kind"])} className="w-48">
          {kinds.map((k) => <option key={k.kind} value={k.kind}>{k.label}</option>)}
        </Select>
        {kind === "host" && (
          <Select value={id} onChange={(e) => setId(e.target.value)}>
            <option value="">choose a host…</option>
            {(hosts.data?.items ?? []).map((h) => <option key={h.id} value={h.id}>{h.name}</option>)}
          </Select>
        )}
        {kind === "group" && (
          <Select value={id} onChange={(e) => setId(e.target.value)}>
            <option value="">choose a pack…</option>
            {(groups.data?.items ?? []).map((g) => <option key={g.id} value={g.id}>{g.name}</option>)}
          </Select>
        )}
        {kind === "cidr" && <Input value={cidr} onChange={(e) => setCidr(e.target.value)} placeholder="192.168.10.0/24 or fd00::/64" />}
        <Button size="sm" onClick={add}>Add</Button>
      </div>
    </div>
  );
}

export function PolicyEditor({ initial, draft, onClose }: { initial?: FwPolicy; draft?: FwPolicyInput; onClose: () => void }) {
  const qc = useQueryClient();
  const services = useFwServices();
  const [p, setP] = useState<FwPolicyInput>(() => initial
    ? { name: initial.name, description: initial.description, enabled: initial.enabled, action: initial.action, from: initial.from, to: initial.to, services: initial.services, position: initial.position }
    : draft ?? { name: "", description: "", enabled: true, action: "allow", from: [], to: [], services: [] });
  const save = useMutation({
    mutationFn: () => initial
      ? unwrap(api.PUT("/firewall/policies/{id}", { params: { path: { id: initial.id } }, body: p }))
      : unwrap(api.POST("/firewall/policies", { body: p })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: fk.all });
      onClose();
    },
  });
  const toggleSvc = (n: string) => setP({ ...p, services: p.services.includes(n) ? p.services.filter((s) => s !== n) : [...p.services, n] });
  // Which hosts this draft changes, and how — before anything is saved.
  const preview = useMutation({
    mutationFn: (draft: FwPolicyInput) => unwrap(api.POST("/firewall/policies/preview", { body: { id: initial?.id, policy: draft } })),
  });
  const ready = p.from.length > 0 && p.to.length > 0 && p.services.length > 0;
  useEffect(() => {
    if (!ready) return;
    const t = setTimeout(() => preview.mutate(p), 400);
    return () => clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [JSON.stringify(p), ready]);
  return (
    <Dialog title={initial ? `Edit policy ${initial.name}` : "New firewall policy"} onClose={onClose} wide>
      <div className="space-y-4 text-sm">
        <p className="text-muted">Say who may reach whom on which services. DROOL compiles this for every affected host, protects the management
          path, and applies it only when you start it from a host or the Firewall page, with automatic rollback protection.</p>
        <div className="grid gap-3 md:grid-cols-[1fr_14rem]">
          <Field label="Name"><Input value={p.name} onChange={(e) => setP({ ...p, name: e.target.value })} placeholder="web-to-database" /></Field>
          <Field label="Action">
            <Select value={p.action} onChange={(e) => setP({ ...p, action: e.target.value as "allow" | "deny" })}>
              <option value="allow">allow</option>
              <option value="deny">deny (wins over allows)</option>
            </Select>
          </Field>
        </div>
        <SelectorPicker label="From" value={p.from} onChange={(from) => setP({ ...p, from })} />
        <SelectorPicker label="To" value={p.to} onChange={(to) => setP({ ...p, to })} />
        <div>
          <div className="mb-1 text-sm font-medium">Services</div>
          <div className="grid gap-1 md:grid-cols-3">
            {(services.data?.items ?? []).map((s) => (
              <Checkbox key={s.name} checked={p.services.includes(s.name)} onChange={() => toggleSvc(s.name)}
                label={<span>{s.name} <span className="text-xs text-muted">{s.description || s.entries.map((e) => `${e.protocol} ${e.ports ?? e.icmp_type ?? ""}`).join(", ")}</span></span>} />
            ))}
          </div>
        </div>
        <Field label="Description (optional)"><Input value={p.description ?? ""} onChange={(e) => setP({ ...p, description: e.target.value })} /></Field>
        <Checkbox checked={p.enabled ?? true} onChange={(v) => setP({ ...p, enabled: v })} label="Enabled" />
        {ready && (
          <div className="rounded-ui border border-line p-3">
            <div className="mb-1 font-medium">Affected hosts {preview.isPending && <span className="text-xs text-muted">(checking…)</span>}</div>
            {preview.error && <Alert>{errorMessage(preview.error)}</Alert>}
            {preview.data && preview.data.items.length === 0 && <p className="text-xs text-muted">No host's policy changes.</p>}
            <ul className="space-y-2">
              {(preview.data?.items ?? []).map((h) => (
                <li key={h.host_id}>
                  <span className="font-medium">{h.host_name}</span>{" "}
                  <span className="text-xs text-muted">mode {h.mode}{h.enforced ? ", enforced" : ""}{h.supported === false ? ", enforcement refused on this host" : ""}</span>
                  {(h.conflicts ?? []).map((c) => <span key={c} className="block text-xs text-danger">refused: {c}</span>)}
                  {(h.added ?? []).map((a) => <span key={"+" + a} className="block text-xs text-ok">+ {a}</span>)}
                  {(h.removed ?? []).map((a) => <span key={"-" + a} className="block text-xs text-danger">− {a}</span>)}
                </li>
              ))}
            </ul>
            <p className="mt-1 text-xs text-muted">Saving changes nothing on hosts; apply it from each host or the Firewall page.</p>
          </div>
        )}
        {save.error && <Alert>{errorMessage(save.error)}</Alert>}
        <div className="flex justify-end gap-2">
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" disabled={save.isPending} onClick={() => save.mutate()}>Save policy</Button>
        </div>
      </div>
    </Dialog>
  );
}

// --- apply ---

export function ApplyDialog({ hostIds, names, remove, changes, onClose }: {
  hostIds: string[]; names: string[]; remove?: boolean; changes?: { added: string[]; removed: string[] }; onClose: () => void;
}) {
  const { can } = useAuth();
  const qc = useQueryClient();
  const [overrideMgmt, setOverrideMgmt] = useState(false);
  const [overrideTimeout, setOverrideTimeout] = useState(false);
  const [allowDrift, setAllowDrift] = useState(false);
  const [reason, setReason] = useState("");
  const [confirmOverride, setConfirmOverride] = useState("");
  const anyOverride = overrideMgmt || overrideTimeout || allowDrift;
  const overrideConfirmed = !overrideMgmt || confirmOverride.trim().toUpperCase() === "OVERRIDE";
  // Several hosts: each one's changes and refusals, reviewed here before
  // anything is applied (DESIGN-STANDARD §5).
  const plans = useQueries({
    queries: (hostIds.length > 1 && !remove ? hostIds : []).map((id) => ({
      queryKey: fk.host(id),
      queryFn: () => unwrap(api.GET("/firewall/hosts/{id}", { params: { path: { id } } })),
    })),
  });
  const apply = useMutation({
    mutationFn: () => unwrap(api.POST("/firewall/apply", {
      body: { host_ids: hostIds, remove, override_mgmt: overrideMgmt, override_timeout: overrideTimeout, allow_drift: allowDrift, reason: anyOverride ? reason : undefined },
    })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: fk.all }),
  });
  return (
    <Dialog title={remove ? "Remove DROOL's firewall policy" : "Apply firewall policy"} onClose={onClose} wide>
      <div className="space-y-3 text-sm">
        <p>{remove ? "Removes DROOL's rules from" : "Applies the current policy to"} <strong>{names.join(", ")}</strong>, one host at a time, stopping at the first failure.</p>
        {changes && !remove && (
          <div className="rounded-ui border border-line p-3">
            <div className="mb-1 font-medium">What changes</div>
            {changes.added.length + changes.removed.length === 0 && <p className="text-xs text-muted">Nothing compared with what is committed (applying again re-verifies it).</p>}
            {changes.added.map((a) => <span key={"+" + a} className="block text-xs text-ok">+ {a}</span>)}
            {changes.removed.map((a) => <span key={"-" + a} className="block text-xs text-danger">− {a}</span>)}
          </div>
        )}
        {!changes && !remove && hostIds.length > 1 && (
          <div className="rounded-ui border border-line p-3" data-testid="apply-review">
            <div className="mb-1 font-medium">What changes, host by host</div>
            <ul className="space-y-1 text-xs">
              {hostIds.map((id, i) => {
                const q = plans[i];
                const pl = q?.data;
                const refused = (pl?.policy.conflicts ?? []).length > 0;
                return (
                  <li key={id}>
                    <strong>{names[i]}</strong>:{" "}
                    {q?.error ? <span className="text-danger">couldn&apos;t load its changes: {errorMessage(q.error)}</span>
                      : !pl ? <span className="text-muted">loading…</span>
                        : refused ? <span className="text-danger">refused: a policy would cut DROOL&apos;s management path</span>
                          : !pl.applied.enforced ? `${pl.explain.length} rules, applied for the first time`
                            : pl.changes_added.length + pl.changes_removed.length === 0 ? <span className="text-muted">no changes (applying again re-verifies it)</span>
                              : <>{pl.changes_added.length > 0 && <span className="text-ok">+{pl.changes_added.length} </span>}{pl.changes_removed.length > 0 && <span className="text-danger">−{pl.changes_removed.length} </span>}
                                rule{pl.changes_added.length + pl.changes_removed.length === 1 ? "" : "s"}</>}
                  </li>
                );
              })}
            </ul>
            <p className="mt-1 text-xs text-muted">Each host&apos;s rules in full are on its firewall page. Nothing changes until you choose Apply.</p>
          </div>
        )}
        <p className="font-medium">Automatic rollback protection</p>
        <ol className="list-decimal space-y-1 pl-5 text-muted">
          <li>DROOL checks the host's firewall environment and measures a brand-new management connection.</li>
          <li>The host writes a durable journal and arms a rollback timer <em>before</em> its rules change, then applies the policy atomically.</li>
          <li>Only a <em>new</em> connection (a fresh agent connection, or a fresh SSH connection through every jump host) can commit it.</li>
          <li>If that never happens — the policy cut the path, the controller vanished, the agent died, the host rebooted — the host restores its previous policy on its own.</li>
        </ol>
        {can("firewall.override") && (
          <details className="rounded-ui border border-line p-3">
            <summary className="cursor-pointer font-medium">Overrides (audited)</summary>
            <div className="mt-2 space-y-2">
              <Checkbox checked={overrideMgmt} onChange={setOverrideMgmt} label="Emergency override: apply even if a deny removes a management path"
                hint="Automatic rollback protection still applies: if the new policy really cuts DROOL off, the host restores the previous policy." />
              <Checkbox checked={overrideTimeout} onChange={setOverrideTimeout} label="Accept a rollback timeout shorter than 3× the measured connection time" />
              <Checkbox checked={allowDrift} onChange={setAllowDrift} label="Replace DROOL rules that were changed outside DROOL (drift)" />
              {overrideMgmt && (
                <div role="alert" className="space-y-2 rounded-ui border-2 border-danger bg-danger-bg p-3 text-fg">
                  <p className="font-semibold text-danger">Emergency override: you are about to apply a policy that DROOL&apos;s own checks say cuts its management path.</p>
                  <ul className="list-disc pl-5 text-xs">
                    <li>DROOL may lose contact with {names.join(", ")} as soon as the rules are applied.</li>
                    <li>Automatic rollback protection still applies: if DROOL can&apos;t commit over a brand-new connection, each host restores its previous policy on its own when its rollback timeout runs out.</li>
                    <li>If the new policy keeps a path DROOL didn&apos;t know about, the override can succeed and leave DROOL depending on it. Use this only to recover from a mistake.</li>
                    <li>The override, your reason and the outcome are recorded in the audit log.</li>
                  </ul>
                  <Field label='Type OVERRIDE to confirm'><Input value={confirmOverride} onChange={(e) => setConfirmOverride(e.target.value)} aria-label="Type OVERRIDE to confirm the emergency override" /></Field>
                </div>
              )}
              {anyOverride && <Field label="Reason (recorded in the audit log)" hint="At least 5 characters."><Input value={reason} onChange={(e) => setReason(e.target.value)} /></Field>}
            </div>
          </details>
        )}
        {apply.error && <Alert>{errorMessage(apply.error)}</Alert>}
        {apply.data ? (
          <Alert tone="ok">Started. Follow it on its <Link className="underline" to={`/jobs/${apply.data.id}`}>run page</Link>; each host&apos;s transaction timeline is on its firewall page.</Alert>
        ) : null}
        <div className="flex justify-end gap-2">
          <Button onClick={onClose}>{apply.data ? "Close" : "Cancel"}</Button>
          {!apply.data && (
            <Button variant={remove || overrideMgmt ? "danger" : "primary"} disabled={apply.isPending || (anyOverride && reason.trim().length < 5) || !overrideConfirmed} onClick={() => apply.mutate()}>
              {remove ? "Remove policy" : overrideMgmt ? "Apply with emergency override" : hostIds.length > 1 ? `Apply to ${hostIds.length} hosts` : "Apply policy"}
            </Button>
          )}
        </div>
      </div>
    </Dialog>
  );
}
