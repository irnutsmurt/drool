import { useState } from "react";
import { Link } from "react-router";
import { useMutation, useQueries, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Checkbox } from "@/components/ui/Checkbox";
import { Dialog } from "@/components/ui/Dialog";
import { Field, Input } from "@/components/ui/Field";
import { errorMessage } from "@/lib/format";
import { applyBlocked, PolicyLines, ruleNames } from "./HostPolicy";
import { modeInfo } from "./parts";
import { fk, type FwPlan } from "./queries";

/** Review and apply (DESIGN-STANDARD §5, review before execution): per
 * host, whether its mode switches to Enforce, what changes, which rules
 * are then in force, and what could affect its connectivity — then one
 * confirmed run, one host at a time, stopping at the first failure. A host
 * the server would refuse before its transaction (no environment, a
 * transaction in progress, a rule cutting the management path, drift) is
 * left out and says why. The mode switch is part of the request
 * (set_enforce): the server makes it only once that host's checks have
 * passed. What was reviewed is what is applied: each host's rules hash goes
 * with the request, and a policy changed meanwhile is refused. Nothing
 * changes until the confirming button. */
export function ApplyDialog({ hostIds, names, remove, onClose }: {
  /** names[i] is hostIds[i]'s name. */
  hostIds: string[]; names: string[]; remove?: boolean; onClose: () => void;
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
  const plans = useQueries({
    queries: (remove ? [] : hostIds).map((id) => ({
      queryKey: fk.host(id),
      queryFn: () => unwrap(api.GET("/firewall/hosts/{id}", { params: { path: { id } } })),
    })),
  });
  // Why the server would refuse a host before its transaction, as far as is
  // known now (it decides again when the run reaches the host).
  const refusal = (pl: FwPlan): string | null => {
    const b = applyBlocked(pl);
    if (b) return b;
    if ((pl.policy.conflicts ?? []).length > 0 && !overrideMgmt) return "a rule would cut DROOL's management path (only the audited emergency override applies it)";
    if (pl.applied.drift && !allowDrift) {
      return can("firewall.override") ? "DROOL's rules there were changed outside DROOL: choose Replace DROOL rules that were changed outside DROOL, under Overrides"
        : "DROOL's rules there were changed outside DROOL: replacing them needs an administrator (an audited override)";
    }
    return null;
  };
  const loaded = remove || plans.every((q) => !!q.data);
  const ready = plans.flatMap((q) => (q.data && !refusal(q.data) ? [q.data] : []));
  const targets = remove ? hostIds : ready.map((pl) => pl.host_id);
  const switching = ready.filter((pl) => pl.config.mode !== "enforce");
  const single = hostIds.length === 1;
  // What was confirmed, kept as it was: the review below refreshes and
  // would otherwise describe the state the run itself produces.
  const [started, setStarted] = useState<{ hosts: string[]; switching: string[] } | null>(null);
  const apply = useMutation({
    mutationFn: () => unwrap(api.POST("/firewall/apply", {
      body: {
        host_ids: targets, remove, override_mgmt: overrideMgmt, override_timeout: overrideTimeout, allow_drift: allowDrift,
        reason: anyOverride ? reason : undefined,
        set_enforce: switching.length > 0 ? true : undefined,
        expect_rules_hashes: remove ? undefined : Object.fromEntries(ready.map((pl) => [pl.host_id, pl.rendered.rules_hash])),
      },
    })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: fk.all }),
  });
  const confirm = () => {
    setStarted({ hosts: remove ? names : ready.map((pl) => pl.host_name), switching: switching.map((pl) => pl.host_name) });
    apply.mutate();
  };
  const n = targets.length;
  const action = remove ? "Remove policy"
    : overrideMgmt ? "Apply with emergency override"
      : switching.length > 0 ? (single ? "Switch to Enforce and apply" : `Switch to Enforce and apply to ${n} host${n === 1 ? "" : "s"}`)
        : single ? "Apply policy" : `Apply to ${n} host${n === 1 ? "" : "s"}`;
  const title = remove ? `Remove DROOL's firewall policy from ${names.join(", ")}?`
    : single ? `Review and apply the firewall policy to ${names[0]}` : `Review and apply the firewall policy to ${hostIds.length} hosts`;
  return (
    <Dialog title={title} onClose={onClose} wide testId="fw-apply-dialog"
      footer={
        <>
          <Button data-autofocus onClick={onClose}>{apply.data ? "Close" : "Cancel"}</Button>
          {!apply.data && (
            <Button variant={remove || overrideMgmt ? "danger" : "primary"}
              disabled={!loaded || n === 0 || apply.isPending || (anyOverride && reason.trim().length < 5) || !overrideConfirmed}
              title={!loaded ? "Loading each host's review…" : n === 0 ? "No host can be applied now: see why above." : undefined}
              onClick={confirm}>
              {action}
            </Button>
          )}
        </>
      }>
      {apply.data && started ? (
        <div className="space-y-3 text-sm" data-testid="apply-started">
          <Alert tone="ok">Started: {remove ? "removing DROOL's policy from" : "applying the saved policy to"} {started.hosts.join(", ")}, one host at a time.
            {started.switching.length > 0 && ` ${started.switching.join(", ")} switch${started.switching.length === 1 ? "es" : ""} to Enforce when ${started.switching.length === 1 ? "its" : "their"} checks have passed, just before the rules change.`}
          </Alert>
          <p>Follow it on its <Link className="underline" to={`/jobs/${apply.data.id}`}>run page</Link>. Each host&apos;s transaction timeline is on its firewall page (Transactions):
            a change that can&apos;t be verified over a fresh management connection is rolled back by the host itself.</p>
        </div>
      ) : (
      <div className="space-y-3 text-sm">
        {remove ? (
          <p>Removes DROOL&apos;s rules from <strong>{names.join(", ")}</strong>, one host at a time, stopping at the first failure. DROOL then no longer filters
            {single ? " this host" : " these hosts"}: every connection its own firewall allows gets through. This is a transaction too, with automatic rollback protection.</p>
        ) : (
          <>
            <p><strong>This is the review: nothing changes until you choose {action}.</strong> Then DROOL applies the saved policy one host at a time, stopping at the first failure.</p>
            <div className="space-y-2" data-testid="apply-review">
              {hostIds.map((id, i) => {
                const q = plans[i];
                if (q?.error) return <p key={id} className="text-danger"><strong>{names[i]}</strong>: couldn&apos;t load its review: {errorMessage(q.error)}</p>;
                if (!q?.data) return <p key={id} className="text-muted"><strong>{names[i]}</strong>: loading its review…</p>;
                return <HostReview key={id} p={q.data} name={names[i]!} refused={refusal(q.data)} single={single} />;
              })}
            </div>
            {loaded && n < hostIds.length && n > 0 && <p className="text-warn">{hostIds.length - n} of {hostIds.length} hosts are left out (see why above). The run applies to the other {n}.</p>}
          </>
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
      </div>
      )}
    </Dialog>
  );
}

/** One host's review: the mode, what changes, the rules then in force,
 * and what could affect its connectivity. */
function HostReview({ p, name, refused, single }: { p: FwPlan; name: string; refused: string | null; single: boolean }) {
  const a = p.applied;
  const switches = p.config.mode !== "enforce";
  const mode = modeInfo[p.config.mode]?.label ?? p.config.mode;
  const rules = ruleNames(p.explain);
  const mgmt = p.explain.filter((r) => r.reason.kind === "management");
  const first = !a.enforced || a.drift === "missing";
  const denies = p.changes_added.filter((l) => l.startsWith("deny "));
  const outRestricted = p.explain.some((r) => r.direction === "out" && r.reason.kind === "default" && r.verdict === "drop");
  const summary = refused ? `left out: ${refused}`
    : [switches ? `switches from ${mode} to Enforce` : "Enforce", first ? `${rules.length} rule${rules.length === 1 ? "" : "s"}, applied for the first time`
      : p.changes_added.length + p.changes_removed.length === 0 ? "no changes (applying again re-verifies it)"
        : `+${p.changes_added.length} −${p.changes_removed.length}`].join(" · ");
  const body = (
    <div className="mt-2 space-y-2">
      <p data-testid="review-mode">
        <span className="font-medium">Mode: </span>
        {switches
          ? <><Badge tone="warn">{mode} → Enforce</Badge> This apply switches the host to Enforce: from then on DROOL&apos;s rules decide which connections it accepts.
            The switch happens only once DROOL&apos;s checks pass, just before the rules change; a host refused before that keeps {mode}.</>
          : "Enforce (already set)."}
      </p>
      <div>
        <span className="font-medium">What changes: </span>
        {first ? <>DROOL starts filtering this host&apos;s traffic with {rules.length === 0 ? "no rules of the saved policy" : <>the saved policy&apos;s rules: {rules.join(", ")}</>}.</>
          : p.changes_added.length + p.changes_removed.length === 0 ? <>Nothing compared with revision {a.revision} (applying again re-verifies it).</>
            : <ul className="mt-1 space-y-0.5 text-xs">
              {p.changes_added.map((l) => <li key={"+" + l} className="text-ok">+ {l}</li>)}
              {p.changes_removed.map((l) => <li key={"-" + l} className="text-danger">− {l}</li>)}
            </ul>}
      </div>
      <div>
        <span className="font-medium">Could affect connectivity:</span>
        <ul className="mt-1 list-disc space-y-0.5 pl-5">
          <li>Inbound connections no rule allows are dropped{rules.length === 0 && ": with no rules, that is every connection except DROOL's management"}. Check that what others use on this host is allowed.</li>
          {outRestricted && <li>Outbound traffic is restricted: only what the rules allow may leave.</li>}
          {denies.map((d) => <li key={d} className="text-danger">New deny: {d}</li>)}
          <li>DROOL&apos;s management path stays open ahead of every rule{mgmt.length > 0 && <>: {[...new Set(mgmt.map((m) => `${m.direction === "in" ? "inbound" : "outbound"} ${m.service} ${m.direction === "in" ? "from" : "to"} ${m.peer}`))].join("; ")}</>}.</li>
          {(p.policy.warnings ?? []).map((w) => <li key={w} className="text-warn">{w}</li>)}
          {(p.management_warnings ?? []).map((w) => <li key={w}>{w}</li>)}
          {!p.eligibility && <li>The firewall environment hasn&apos;t been checked yet: DROOL checks it first and stops if it can&apos;t enforce here.</li>}
          {(p.policy.conflicts ?? []).map((c) => <li key={c.message} className="text-danger">{c.message}</li>)}
        </ul>
      </div>
      <details>
        <summary className="cursor-pointer font-medium">The rules in force after applying</summary>
        <div className="mt-2"><PolicyLines rows={p.explain} hostName={name} compact /></div>
      </details>
    </div>
  );
  return (
    <section className="rounded-ui border border-line p-3" aria-label={`${name}: review`} data-testid="host-review">
      {single ? (
        <>
          <h3 className="font-semibold">{name}</h3>
          {refused && <p className="mt-1 text-danger">Left out: {refused}.</p>}
          {body}
        </>
      ) : (
        <details>
          <summary className="cursor-pointer"><strong>{name}</strong>: <span className={refused ? "text-danger" : ""}>{summary}</span></summary>
          {body}
        </details>
      )}
    </section>
  );
}
