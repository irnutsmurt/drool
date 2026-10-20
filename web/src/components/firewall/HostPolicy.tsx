import { useState, type ReactNode } from "react";
import { Link } from "react-router";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button, primaryLink } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { Checkbox } from "@/components/ui/Checkbox";
import { cx } from "@/components/ui/cx";
import { plainLink } from "@/components/ui/links";
import { Table } from "@/components/ui/Table";
import { useHostNetwork } from "@/components/network/queries";
import { errorMessage, formatAgo } from "@/lib/format";
import { modeInfo } from "./parts";
import type { FwAllowance, FwPlan } from "./queries";

// A host's firewall, as the saved policy resolves for it (the compiler's
// Explain), kept apart from what DROOL has committed on the host and from
// what was observed there (DESIGN-STANDARD §8). The server decides every
// state shown here (applied, drift, rules_changed, the transaction, the
// environment); these components word them.

type Tone = "ok" | "warn" | "danger" | "neutral" | "accent";

/** The rules of the saved policy that apply to this host, by name. */
export function ruleNames(rows: FwAllowance[]): string[] {
  return [...new Set(rows.filter((r) => r.reason.kind === "policy").map((r) => r.reason.policy_name || r.reason.text))];
}

/** The policy is in force on the host: committed, and its rules not
 * missing (changed outside DROOL is in force, but unknown). */
export const policyInForce = (p: FwPlan) => p.applied.enforced && p.applied.drift !== "missing";

/** What an apply would do now: something to apply (the first time, saved
 * changes, rules to put back), not just a re-verification. */
export const needsApply = (p: FwPlan) => !p.applied.enforced || p.applied.rules_changed || !!p.applied.drift;

/** Why applying can't work on this host right now (the server would
 * refuse), or nothing. Conflicts aren't here: an administrator may still
 * use the audited emergency override. */
export function applyBlocked(p: FwPlan): string | null {
  if (p.pending_transaction) return `a firewall transaction (${p.pending_transaction.id}) is still ${p.pending_transaction.state} on this host`;
  if (p.eligibility && !p.eligibility.supported) return `DROOL can't enforce on this host: ${(p.eligibility.reasons ?? []).join("; ") || "the environment check refused it"}`;
  if (p.transport === "none" || p.transport === "blocked") return "DROOL has no management path to this host right now";
  return null;
}

/** The host's policy state in one line: what is in force, and what the
 * person should know first. */
export function policyState(p: FwPlan): { tone: Tone; icon: string; title: string; text: string } {
  const a = p.applied;
  const mode = modeInfo[p.config.mode]?.label ?? p.config.mode;
  const tx = p.pending_transaction;
  if (tx) {
    return { tone: "warn", icon: "◷", title: "A firewall change is in progress",
      text: `Transaction ${tx.id} is ${tx.state}: the host commits it over a fresh management connection, or restores its previous rules by itself when its rollback timeout runs out.` };
  }
  if (a.enforced && a.drift === "missing") {
    return { tone: "danger", icon: "⚠", title: "DROOL's rules are missing from this host",
      text: `Revision ${a.revision} was committed, but its rules were removed outside DROOL (for example by nft or a reload of nftables.service): DROOL doesn't filter this host now.` };
  }
  if (a.enforced && a.drift) {
    return { tone: "danger", icon: "⚠", title: "Changed outside DROOL: what this host enforces isn't known",
      text: `DROOL's rules on this host (revision ${a.revision}) were changed outside DROOL, so DROOL can't tell which connections it allows now.` };
  }
  if (!a.enforced) {
    if (ruleNames(p.explain).length === 0) {
      return { tone: "neutral", icon: "◌", title: "No firewall rules for this host yet",
        text: "The saved policy has no rules for this host. Applied as it is, it would drop every inbound connection except DROOL's own management path." };
    }
    return { tone: "warn", icon: "◌", title: "Policy saved — not yet enforced.",
      text: p.config.mode === "enforce"
        ? "Enforce is set, but the policy hasn't been applied: DROOL doesn't filter this host yet."
        : `DROOL doesn't filter this host (mode ${mode}). Below is what the saved policy would do here once it is applied.` };
  }
  if (a.rules_changed) {
    return { tone: "warn", icon: "↻", title: "Saved changes not applied yet",
      text: `The host still runs revision ${a.revision}; the saved policy differs from it.` };
  }
  if (a.sets_pending) {
    return { tone: "accent", icon: "↻", title: `In force: revision ${a.revision}, addresses updating`,
      text: "DROOL's policy is in force on this host. Newer addresses are being sent to it automatically: there is nothing to apply." };
  }
  return { tone: "ok", icon: "✓", title: `In force: revision ${a.revision}`, text: "What the saved policy says for this host is what DROOL enforces on it." };
}

/** The top of a host's firewall page: the policy's state, saved policy,
 * applied policy and observed traffic each labelled, and the one next
 * action (review and apply, which says whether it switches the mode). */
export function PolicyStatus({ p, onShowObserved, onApply, onRemove }: {
  p: FwPlan; onShowObserved: () => void; onApply: () => void; onRemove: () => void;
}) {
  const { can } = useAuth();
  const st = policyState(p);
  const a = p.applied;
  const blocked = applyBlocked(p);
  const conflicts = p.policy.conflicts ?? [];
  const write = can("firewall.write");
  const names = ruleNames(p.explain);
  // One obvious next action: applying, when there is something to apply,
  // nothing refuses it and the policy has rules for this host; otherwise
  // what fixes it (the Designer), with the review a secondary step.
  const review = write && !blocked && (needsApply(p) || a.enforced);
  const primary = review && needsApply(p) && conflicts.length === 0 && names.length > 0;
  const label = !needsApply(p) ? "Apply again…" : !a.enforced ? "Review and apply policy…" : a.drift ? "Review and apply again…" : "Review and apply changes…";
  return (
    <Card title="Policy on this host">
      <div className="space-y-3 text-sm" data-testid="fw-policy-status">
        <div className="flex flex-wrap items-start gap-x-3 gap-y-1">
          <Badge tone={st.tone} wrap><span aria-hidden="true" className="mr-1">{st.icon}</span>{st.title}</Badge>
          <p className="min-w-0 flex-1 basis-64">{st.text}</p>
        </div>
        <dl className="grid gap-x-4 gap-y-2 sm:grid-cols-[10rem_1fr]">
          <dt className="font-medium">Saved policy</dt>
          <dd data-testid="fw-saved">
            {names.length === 0 ? "No rules for this host." : <>{names.length} rule{names.length === 1 ? "" : "s"} for this host: {names.slice(0, 4).join(", ")}{names.length > 4 ? `, and ${names.length - 4} more` : ""}.</>}
            {" "}DROOL&apos;s management path is always allowed. <Link className={plainLink} to="/firewall?tab=designer&view=rules">Firewall Designer</Link>
          </dd>
          <dt className="font-medium">Applied on the host</dt>
          <dd data-testid="fw-applied"><AppliedLine p={p} /></dd>
          <dt className="font-medium">Observed traffic</dt>
          <dd data-testid="fw-observed"><ObservedLine hostId={p.host_id} onShow={onShowObserved} /></dd>
        </dl>
        {blocked && !(p.pending_transaction) && <Alert tone="warn">Applying isn&apos;t possible right now: {blocked}.{p.eligibility && !p.eligibility.supported && " See Mode and environment, below."}</Alert>}
        {conflicts.length > 0 && (
          <Alert><strong>Applying is refused: a rule would cut DROOL off from this host.</strong>
            {conflicts.map((c) => <span key={c.message} className="block">• {c.message}</span>)}
            <span className="mt-1 block">Change the rule in the Firewall Designer. An administrator&apos;s audited emergency override is still protected by automatic rollback.</span></Alert>
        )}
        {p.config.mode !== "enforce" && a.enforced && (
          <Alert tone="warn">DROOL&apos;s last committed policy (revision {a.revision}) is still in force on this host: the mode ({modeInfo[p.config.mode]?.label ?? p.config.mode}) doesn&apos;t remove it.
            Use <strong>Remove DROOL policy…</strong> to stop filtering, or review and apply the policy to switch back to Enforce and update it.</Alert>
        )}
        {a.drift && (
          <p className="text-xs text-muted">Applying again replaces what was changed: the review offers <strong>Replace DROOL rules that were changed outside DROOL</strong> (an audited override, administrators).</p>
        )}
        {write && (
          <div className="flex flex-wrap gap-2">
            {!primary && (conflicts.length > 0 || (names.length === 0 && !a.enforced)) && (
              <Link className={primaryLink} to="/firewall?tab=designer&view=rules">{conflicts.length > 0 ? "Fix the rule in the Firewall Designer" : "Add a rule in the Firewall Designer"}</Link>
            )}
            {review && <Button variant={primary ? "primary" : "secondary"} onClick={onApply}>{label}</Button>}
            {a.enforced && !p.pending_transaction && <Button variant="ghost" onClick={onRemove}>Remove DROOL policy…</Button>}
          </div>
        )}
      </div>
    </Card>
  );
}

function AppliedLine({ p }: { p: FwPlan }) {
  const a = p.applied;
  const via = p.transport === "agent" ? "the Shepherd" : p.transport === "ssh" ? "SSH" : null;
  let text: ReactNode;
  if (!a.enforced) text = "Nothing of DROOL's: DROOL doesn't filter this host's traffic.";
  else if (a.drift === "missing") text = `Revision ${a.revision} was committed; its rules are missing from the host now.`;
  else if (a.drift) text = `Unknown: revision ${a.revision} was committed, then DROOL's rules were changed outside DROOL.`;
  else if (a.rules_changed) text = `Revision ${a.revision}, which differs from the saved policy.`;
  else text = `Revision ${a.revision}, the same as the saved policy.`;
  return (
    <>
      {text}
      {a.override_mgmt && <> <Badge tone="danger">applied with the emergency override</Badge></>}
      {a.enforced && (
        <span className="block text-xs text-muted">
          {a.status_at ? `DROOL last read the host's firewall state ${formatAgo(a.status_at)}` : "DROOL hasn't read the host's firewall state since then"}
          {via && <> · managed over {via}</>}
          {a.sets_pending && " · newer addresses are being propagated"}
          {a.sets_error && <span className="text-danger"> · the last address update failed: {a.sets_error}</span>}
        </span>
      )}
    </>
  );
}

function ObservedLine({ hostId, onShow }: { hostId: string; onShow: () => void }) {
  const { can } = useAuth();
  const net = useHostNetwork(hostId, can("network.read"));
  if (!can("network.read")) return <span className="text-muted">Not shown: viewing observed connections needs the network.read permission.</span>;
  if (net.error) return <span className="text-danger">Couldn&apos;t load the observations: {errorMessage(net.error)}</span>;
  const hn = net.data;
  if (!hn) return <span className="text-muted">Loading…</span>;
  const c = hn.counts;
  const never = hn.collection.mode === "snapshot" && !hn.collection.snapshot_at && c.flows === 0;
  return (
    <>
      {never ? "No snapshot taken yet: SSH hosts are observed only when you take one."
        : <>{c.flows} connection{c.flows === 1 ? "" : "s"} seen{c.unclassified > 0 && <>, {c.unclassified} not in the policy</>}.</>}
      {" "}<button type="button" className={plainLink} onClick={onShow}>Observed connections</button>
      <span className="block text-xs text-muted">
        {hn.collection.label}{hn.collection.stale && <span className="text-warn"> · ⚠ no report since {formatAgo(hn.collection.reported_at)}</span>}.
        {" "}What DROOL saw, not what the firewall allowed or blocked.
      </span>
    </>
  );
}

const rowKey = (r: FwAllowance) => [r.direction, r.verdict, r.service, r.peer, r.reason.kind, r.reason.policy_id ?? "", r.reason.text].join("|");

/** The saved policy resolved for this host — or what DROOL committed there
 * — readable: per direction, the default and the rules, with the named
 * services, the peers and the rule each line comes from. Where the saved
 * policy differs from what the host runs, both can be shown, and the
 * lines that differ are marked. */
export function ResolvedPolicy({ p, hostName }: { p: FwPlan; hostName: string }) {
  const a = p.applied;
  const inForce = policyInForce(p);
  const committed = p.applied_explain ?? [];
  // The saved rules differ from what the host runs (the server compares
  // them): both can be shown, and the lines that differ are marked.
  const both = inForce && a.rules_changed && committed.length > 0;
  const [view, setView] = useState<"saved" | "applied">("saved");
  const showApplied = both && view === "applied";
  const driftOnly = inForce && !!a.drift && !a.rules_changed;
  const rows = showApplied || (driftOnly && committed.length > 0) ? committed : p.explain;
  const other = both ? new Set((showApplied ? p.explain : committed).map(rowKey)) : null;
  const title = !inForce ? "Policy preview" : both && !showApplied ? "Saved policy — not applied yet" : a.drift ? "Applied policy (not verified)" : "Applied policy";
  let description: ReactNode;
  if (!inForce) {
    description = <>What the saved policy would do on {hostName} once it is applied, as DROOL compiles it for this host. <strong>Nothing of it is enforced now.</strong></>;
  } else if (driftOnly) {
    description = <>What DROOL committed: revision {a.revision}. Its rules on the host were changed outside DROOL since, so the host may do something else now.</>;
  } else if (showApplied) {
    description = <>What {hostName} runs now: revision {a.revision}{a.drift ? ", changed outside DROOL since (not verified)" : ""}. Lines marked ↻ change when the saved policy is applied.</>;
  } else if (both) {
    description = <>The saved policy, as it will be applied: <strong>not in force yet</strong> (the host runs revision {a.revision}). Lines marked ↻ aren&apos;t on the host yet.</>;
  } else {
    description = <>What DROOL enforces on {hostName} now: revision {a.revision}, the same as the saved policy.</>;
  }
  return (
    <Card title={title} actions={both && (
      <div role="group" aria-label="Show" className="inline-flex overflow-hidden rounded-ui border border-line text-xs">
        {(["saved", "applied"] as const).map((v) => (
          <button key={v} type="button" aria-pressed={view === v} onClick={() => setView(v)}
            className={cx("px-2.5 py-1 pointer-coarse:min-h-11", view === v ? "bg-accent text-accent-fg" : "bg-surface hover:bg-surface-2")}>
            {v === "saved" ? "Saved policy (not applied)" : `In force now (revision ${a.revision})`}
          </button>
        ))}
      </div>
    )}>
      <div className="space-y-4 text-sm" data-testid="fw-resolved">
        <p className="text-muted">{description}</p>
        <PolicyLines rows={rows} hostName={hostName} mark={other ? (r) => !other.has(rowKey(r)) : undefined}
          markLabel={showApplied ? "changes when applied" : "not applied yet"} />
        {!showApplied && (p.policy.warnings ?? []).map((w) => <p key={w} className="text-xs text-warn"><span aria-hidden="true">⚠ </span>{w}</p>)}
        {(p.management_warnings ?? []).map((w) => <p key={w} className="text-xs text-muted">{w}</p>)}
      </div>
    </Card>
  );
}

/** A compiled policy's lines, inbound then outbound, each with its default.
 * Base rules (replies, loopback, ICMP, DHCP) on request. */
export function PolicyLines({ rows, hostName, mark, markLabel = "not applied yet", compact }: {
  rows: FwAllowance[]; hostName: string; mark?: (r: FwAllowance) => boolean; markLabel?: string; compact?: boolean;
}) {
  const [showBase, setShowBase] = useState(false);
  const section = (dir: "in" | "out") => {
    const def = rows.find((r) => r.direction === dir && r.reason.kind === "default");
    const lines = rows.filter((r) => r.direction === dir && r.reason.kind !== "default" && (showBase || r.reason.kind !== "base"));
    const heading = dir === "in" ? `Inbound: connections to ${hostName}` : `Outbound: connections from ${hostName}`;
    const defText = !def ? "" : dir === "in"
      ? def.verdict === "drop" ? "Everything not allowed below is dropped." : "Allowed unless a rule below denies it."
      : def.verdict === "drop" ? "Restricted: only what is allowed below (and DROOL's management) may leave." : "Not restricted: allowed unless a rule below denies it.";
    return (
      <section key={dir} aria-label={heading} className="space-y-1">
        <h3 className="font-semibold">{heading}</h3>
        {def && <p className="text-muted" data-testid={`fw-default-${dir}`}>Default: {defText}{mark?.(def) && <> <MarkBadge label={markLabel} /></>}</p>}
        <Table rows={lines} rowKey={rowKey} caption={heading}
          empty={dir === "in" ? "No rule for inbound connections." : "No rule for outbound connections."}
          columns={[
            { header: "Allow or deny", cell: (r) => <Verdict r={r} /> },
            { header: "Service", cell: (r) => <ServiceText r={r} />, primary: true },
            { header: dir === "in" ? "From" : "To", cell: (r) => (
              <span>{r.peer}{r.members && r.members.length > 0 && <span className="block text-xs text-muted">{r.members.join(", ")}</span>}</span>
            ) },
            { header: "Why", cell: (r) => (
              <span className="inline-flex flex-wrap items-center gap-1">
                {r.reason.kind === "policy" ? <>rule <strong>{r.reason.policy_name || r.reason.text}</strong></>
                  : r.reason.kind === "management" ? <span>{r.reason.text}: protected, ahead of every rule</span>
                    : <span className="text-muted">base rule</span>}
                {mark?.(r) && <MarkBadge label={markLabel} />}
              </span>
            ) },
          ]} />
      </section>
    );
  };
  return (
    <div className={cx("space-y-4", compact && "space-y-3")}>
      {section("in")}
      {section("out")}
      <Checkbox checked={showBase} onChange={setShowBase} label="Show base rules (replies, loopback, ICMP, DHCP)" />
    </div>
  );
}

function MarkBadge({ label }: { label: string }) {
  return <Badge tone="warn"><span aria-hidden="true" className="mr-1">↻</span>{label}</Badge>;
}

function Verdict({ r }: { r: FwAllowance }) {
  if (r.reason.kind === "management") return <Badge tone="accent"><span aria-hidden="true" className="mr-1">⛨</span>DROOL management</Badge>;
  return r.verdict === "accept"
    ? <Badge tone="ok"><span aria-hidden="true" className="mr-1">✓</span>Allow</Badge>
    : <Badge tone="danger"><span aria-hidden="true" className="mr-1">✕</span>Deny</Badge>;
}

/** "esphome · TCP 6053": the named services, then what they are. */
function ServiceText({ r }: { r: FwAllowance }) {
  const names = r.services ?? [];
  if (names.length === 0) return <span>{r.service}</span>;
  return <span><strong className="font-medium">{names.join(", ")}</strong> <span className="text-muted">· {r.service}</span></span>;
}
