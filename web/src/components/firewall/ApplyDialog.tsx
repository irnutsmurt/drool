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
import { Field, Input, Select } from "@/components/ui/Field";
import { errorMessage } from "@/lib/format";
import { actionLabel, decide, plan, type Decision, type Mode, type ModeChoice } from "./applyChoices";
import { applyBlocked, installsNftables, PolicyLines, ruleNames } from "./HostPolicy";
import { InstallNftables, useEnvironmentCheck } from "./InstallNftables";
import { modeInfo } from "./parts";
import { fk, type FwPlan } from "./queries";

const modeLabel = (m: string) => modeInfo[m]?.label ?? m;

/** Review and apply (DESIGN-STANDARD §5, review before execution): per
 * host, its mode, what changes, which rules are then in force, and what
 * could affect its connectivity — then one confirmed run, one host at a
 * time, stopping at the first failure. A host the server would refuse
 * before its transaction (no environment, a transaction in progress, a rule
 * cutting the management path, drift) is left out and says why.
 *
 * A host that isn't in Enforce mode is applied to only if Enforce is chosen
 * for it here: the review asks, offering DROOL's modes with "Keep current
 * mode" first and chosen. Choosing Off, Monitor or Guided changes only the
 * mode. The switch to Enforce is part of the request (set_enforce_hosts):
 * the server makes it only once that host's checks have passed. What was
 * reviewed is what is applied: each host's rules hash goes with the
 * request, and a policy changed meanwhile is refused. Nothing changes until
 * the confirming button. */
export function ApplyDialog({ hostIds, names, remove, initialModes, onClose }: {
  /** names[i] is hostIds[i]'s name. */
  hostIds: string[]; names: string[]; remove?: boolean;
  /** Modes already chosen for hosts (Enforce, chosen on the host's page). */
  initialModes?: Record<string, ModeChoice>;
  onClose: () => void;
}) {
  const { can } = useAuth();
  const qc = useQueryClient();
  const [overrideMgmt, setOverrideMgmt] = useState(false);
  const [overrideTimeout, setOverrideTimeout] = useState(false);
  const [allowDrift, setAllowDrift] = useState(false);
  const [reason, setReason] = useState("");
  const [confirmOverride, setConfirmOverride] = useState("");
  const [choices, setChoices] = useState<Record<string, ModeChoice>>(initialModes ?? {});
  // nftables installed from the review (DESIGN-STANDARD §1, Workflow first):
  // the run started for each host, kept so its outcome stays in sight.
  const [nftJobs, setNftJobs] = useState<Record<string, string>>({});
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
  // Reviewed on what the server says now, not on what a page cached
  // earlier: a review is shown once its plan was fetched for this dialog.
  const fresh = (q: (typeof plans)[number]) => !!q.data && q.isFetchedAfterMount;
  const loaded = remove || plans.every(fresh);
  const shown = plans.flatMap((q) => (fresh(q) ? [q.data!] : []));
  const decisions = shown.map((pl) => decide(pl, choices[pl.host_id], refusal(pl)));
  const run = plan(shown, decisions);
  const targets = remove ? hostIds : run.apply.map((pl) => pl.host_id);
  const single = hostIds.length === 1;
  // What was confirmed, kept as it was: the review below refreshes and
  // would otherwise describe the state the run itself produces.
  const [started, setStarted] = useState<{ hosts: string[]; switching: string[]; modes: string[] } | null>(null);
  const apply = useMutation({
    mutationFn: async () => {
      // Mode-only changes first (each one audited), then the run. A failure
      // stops before anything is applied, and says what changed.
      const done: string[] = [];
      for (const { p, to } of remove ? [] : run.modes) {
        try {
          await unwrap(api.PUT("/firewall/hosts/{id}", { params: { path: { id: p.host_id } },
            body: { mode: to, outbound: p.config.outbound ?? "allow", rollback_timeout_s: p.config.rollback_timeout_s ?? 0 } }));
          done.push(`${p.host_name} is now in ${modeLabel(to)} mode`);
        } catch (e) {
          throw new Error(`${done.length ? done.join("; ") + ". " : ""}Couldn't change ${p.host_name}'s mode: ${errorMessage(e)}. Nothing was applied.`);
        }
      }
      const job = targets.length === 0 ? null : await unwrap(api.POST("/firewall/apply", {
        body: {
          host_ids: targets, remove, override_mgmt: overrideMgmt, override_timeout: overrideTimeout, allow_drift: allowDrift,
          reason: anyOverride ? reason : undefined,
          set_enforce_hosts: remove || run.switching.length === 0 ? undefined : run.switching.map((pl) => pl.host_id),
          expect_rules_hashes: remove ? undefined : Object.fromEntries(run.apply.map((pl) => [pl.host_id, pl.rendered.rules_hash])),
        },
      }));
      return { job, modes: done };
    },
    onSettled: () => void qc.invalidateQueries({ queryKey: fk.all }),
  });
  const confirm = () => {
    setStarted({ hosts: remove ? names : run.apply.map((pl) => pl.host_name), switching: run.switching.map((pl) => pl.host_name),
      modes: run.modes.map(({ p, to }) => `${p.host_name}: ${modeLabel(p.config.mode)} → ${modeLabel(to)}`) });
    apply.mutate();
  };
  const nothing = !remove && run.apply.length === 0 && run.modes.length === 0;
  const action = remove ? "Remove policy" : overrideMgmt && run.apply.length > 0 ? "Apply with emergency override" : actionLabel(run, modeLabel);
  const title = remove ? `Remove DROOL's firewall policy from ${names.join(", ")}?`
    : single ? `Review and apply the firewall policy to ${names[0]}` : `Review and apply the firewall policy to ${hostIds.length} hosts`;
  const r = apply.data;
  return (
    <Dialog title={title} onClose={onClose} wide testId="fw-apply-dialog"
      footer={
        <>
          <Button data-autofocus onClick={onClose}>{r ? "Close" : "Cancel"}</Button>
          {!r && (
            <Button variant={remove || (overrideMgmt && run.apply.length > 0) ? "danger" : "primary"}
              disabled={!loaded || nothing || apply.isPending || (anyOverride && reason.trim().length < 5) || !overrideConfirmed}
              title={!loaded ? "Loading each host's review…" : nothing ? "Nothing to do yet: choose a mode above, or see why each host is left out." : undefined}
              onClick={confirm}>
              {action}
            </Button>
          )}
        </>
      }>
      {r && started ? (
        <div className="space-y-3 text-sm" data-testid="apply-started">
          {r.modes.length > 0 && (
            <Alert tone="ok">Mode changed: {started.modes.join("; ")}. Nothing was applied to {r.modes.length === 1 ? "that host" : "those hosts"}: Off, Monitor and Guided don&apos;t enforce.</Alert>
          )}
          {r.job && (
            <>
              <Alert tone="ok">Started: {remove ? "removing DROOL's policy from" : "applying the saved policy to"} {started.hosts.join(", ")}, one host at a time.
                {started.switching.length > 0 && ` ${started.switching.join(", ")} switch${started.switching.length === 1 ? "es" : ""} to Enforce when ${started.switching.length === 1 ? "its" : "their"} checks have passed, just before the rules change.`}
              </Alert>
              <p>Follow it on its <Link className="underline" to={`/jobs/${r.job.id}`}>run page</Link>. Each host&apos;s transaction timeline is on its firewall page (Transactions):
                a change that can&apos;t be verified over a fresh management connection is rolled back by the host itself.</p>
            </>
          )}
        </div>
      ) : (
      <div className="space-y-3 text-sm">
        {remove ? (
          <p>Removes DROOL&apos;s rules from <strong>{names.join(", ")}</strong>, one host at a time, stopping at the first failure. DROOL then no longer filters
            {single ? " this host" : " these hosts"}: every connection its own firewall allows gets through. This is a transaction too, with automatic rollback protection.</p>
        ) : (
          <>
            <p><strong>This is the review: nothing changes until you choose {action}.</strong> {run.apply.length > 0 || !loaded
              ? "Then DROOL applies the saved policy one host at a time, stopping at the first failure."
              : "The saved policy is applied only to hosts in Enforce mode, or where you choose Enforce below."}</p>
            <div className="space-y-2" data-testid="apply-review">
              {hostIds.map((id, i) => {
                const q = plans[i];
                if (q?.error) return <p key={id} className="text-danger"><strong>{names[i]}</strong>: couldn&apos;t load its review: {errorMessage(q.error)}</p>;
                if (!q || !fresh(q)) return <p key={id} className="text-muted"><strong>{names[i]}</strong>: loading its review…</p>;
                const at = shown.findIndex((pl) => pl.host_id === id);
                return <HostReview key={id} p={q.data!} name={names[i]!} single={single} decision={decisions[at]!} refused={refusal(q.data!)}
                  choice={choices[id] ?? "keep"} canChoose={can("firewall.write")} onChoose={(c) => setChoices((cur) => ({ ...cur, [id]: c }))}
                  nft={<NftablesStep p={q.data!} name={names[i]!} jobId={nftJobs[id]} onStarted={(j) => setNftJobs((cur) => ({ ...cur, [id]: j }))} />} />;
              })}
            </div>
            {loaded && !single && run.untouched.length > 0 && (run.apply.length > 0 || run.modes.length > 0) && (
              <p className="text-muted">{run.untouched.length} of {hostIds.length} hosts stay as they are (see why above).</p>
            )}
          </>
        )}
        {(remove || run.apply.length > 0) && (
          <>
            <p className="font-medium">Automatic rollback protection</p>
            <ol className="list-decimal space-y-1 pl-5 text-muted">
              <li>DROOL checks the host&apos;s firewall environment and measures a brand-new management connection.</li>
              <li>The host writes a durable journal and arms a rollback timer <em>before</em> its rules change, then applies the policy atomically.</li>
              <li>Only a <em>new</em> connection (a fresh agent connection, or a fresh SSH connection through every jump host) can commit it.</li>
              <li>If that never happens — the policy cut DROOL&apos;s path, the controller vanished, the agent died, the host rebooted — the host restores its previous policy on its own.</li>
            </ol>
          </>
        )}
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

/** One host's review: its mode (and, when it isn't Enforce, the choice of
 * mode), then — where the policy is applied — what changes, what could
 * affect its connectivity, and the rules in force after applying. */
function HostReview({ p, name, single, decision, refused, choice, canChoose, onChoose, nft }: {
  p: FwPlan; name: string; single: boolean; decision: Decision; refused: string | null;
  choice: ModeChoice; canChoose: boolean; onChoose: (c: ModeChoice) => void;
  /** Installing nftables first, where that is all enforcing needs. */
  nft?: React.ReactNode;
}) {
  const a = p.applied;
  const current = p.config.mode;
  const rules = ruleNames(p.explain);
  const first = !a.enforced || a.drift === "missing";
  const applying = decision.kind === "apply";
  const summary = decision.kind === "mode" ? `mode changes to ${modeLabel(decision.to)}, nothing applied`
    : decision.kind === "none" ? (current === "enforce" ? `left out: ${decision.why}` : `stays ${modeLabel(current)}, not applied`)
      : [decision.switch ? `switches from ${modeLabel(current)} to Enforce` : "Enforce", first ? `${rules.length} rule${rules.length === 1 ? "" : "s"}, applied for the first time`
        : p.changes_added.length + p.changes_removed.length === 0 ? "no changes (applying again re-verifies it)"
          : `+${p.changes_added.length} −${p.changes_removed.length}`].join(" · ");
  const body = <ApplyBody p={p} name={name} />;
  return (
    <section className="space-y-2 rounded-ui border border-line p-3" aria-label={`${name}: review`} data-testid="host-review">
      <h3 className="font-semibold">{name}{!single && <>: <span className={summaryTone(decision, current)} data-testid="host-review-summary">{summary}</span></>}</h3>
      {current === "enforce" ? (
        <>
          {refused && <p className="text-danger">Left out: {refused}.</p>}
          {!refused && <p data-testid="review-mode"><span className="font-medium">Mode: </span>Enforce (already set).</p>}
        </>
      ) : (
        <ModeChoiceField p={p} name={name} choice={choice} refused={refused} canChoose={canChoose} onChoose={onChoose} decision={decision} />
      )}
      {nft}
      {applying && (single ? body : (
        <details>
          <summary className="cursor-pointer font-medium">What applying changes on {name}</summary>
          {body}
        </details>
      ))}
      {(p.address_only_rules ?? []).length > 0 && (
        <ul className="space-y-0.5 text-xs text-warn" data-testid="review-address-only">
          {p.address_only_rules.map((n) => <li key={n.rule}><span aria-hidden="true">⚠ </span>{n.message}</li>)}
        </ul>
      )}
    </section>
  );
}

/** nftables, offered in the review where it is the only thing keeping
 * DROOL from enforcing: the ordinary package run (dry run, confirmation,
 * audit), and the environment checked again afterwards. Installing changes
 * no mode and enforces nothing: the mode is still chosen above. */
function NftablesStep({ p, name, jobId, onStarted }: { p: FwPlan; name: string; jobId?: string; onStarted: (jobId: string) => void }) {
  const env = useEnvironmentCheck(p.host_id);
  const needed = installsNftables(p);
  if (!needed && !jobId) return null;
  return (
    <div className="rounded-ui border border-line p-3" data-testid="review-needs-nftables">
      {needed && (
        <p><strong>nftables isn&apos;t installed on {name}</strong>, so DROOL can&apos;t enforce there yet. DROOL can install it with {name}&apos;s package
          manager: you see what that involves first. Once it is installed, choose the mode above.</p>
      )}
      <InstallNftables hostId={p.host_id} hostName={name} mode={p.config.mode} jobId={jobId} onStarted={onStarted}
        onInstalled={env.recheck} checking={env.checking} checkFailed={env.failed} />
    </div>
  );
}

const summaryTone = (d: Decision, current: string) => (d.kind === "none" && current === "enforce" ? "text-danger" : d.kind === "apply" ? "" : "text-muted");

/** The mode prompt for a host that isn't in Enforce mode: its mode now,
 * DROOL's modes with "Keep current mode" first, what the choice does. */
function ModeChoiceField({ p, name, choice, refused, canChoose, onChoose, decision }: {
  p: FwPlan; name: string; choice: ModeChoice; refused: string | null; canChoose: boolean; onChoose: (c: ModeChoice) => void; decision: Decision;
}) {
  const current = p.config.mode;
  const a = p.applied;
  return (
    <div className="space-y-2" data-testid="review-mode">
      <p>
        <span className="font-medium">Mode: </span><Badge tone="neutral">{modeLabel(current)}</Badge>{" "}
        {current === "off" ? "Firewall management isn't enabled on this host. Would you like to configure it?"
          : "DROOL observes this host but doesn't enforce the policy on it. Would you like to configure it?"}
      </p>
      <div className="max-w-md">
        <Field label={`Mode for ${name}`} hint={choice === "keep" ? `${modeLabel(current)}: ${modeInfo[current]?.short ?? ""}` : modeInfo[choice]?.text}>
          <Select value={choice} disabled={!canChoose} onChange={(e) => onChoose(e.target.value as ModeChoice)} data-testid="review-mode-choice">
            <option value="keep">Keep current mode ({modeLabel(current)})</option>
            {(["off", "monitor", "guided", "enforce"] as Mode[]).filter((m) => m !== current).map((m) => (
              <option key={m} value={m} disabled={m === "enforce" && !!refused}>
                {modeLabel(m)} — {modeInfo[m]!.short}{m === "enforce" && refused ? " (not possible now)" : ""}
              </option>
            ))}
          </Select>
        </Field>
      </div>
      <p className={decision.kind === "apply" ? "" : "text-muted"} data-testid="review-mode-outcome">
        {decision.kind === "apply" ? <><Badge tone="warn">{modeLabel(current)} → Enforce</Badge> This apply switches the host to Enforce: from then on DROOL&apos;s rules decide which connections it accepts.
          The switch happens only once DROOL&apos;s checks pass, just before the rules change; a host refused before that keeps {modeLabel(current)}.</>
          : decision.kind === "mode" ? <>When you confirm, {name}&apos;s mode changes to {modeLabel(decision.to)}. Nothing is applied or enforced{decision.to === "off" ? ", and DROOL stops observing it" : ""}.</>
            : <>Nothing changes on {name}: the saved policy isn&apos;t applied while it is in {modeLabel(current)} mode.{refused && <> Enforce isn&apos;t possible now: {refused}.</>}</>}
        {decision.kind !== "apply" && a.enforced && <> DROOL&apos;s revision {a.revision} stays in force on it: a mode doesn&apos;t remove DROOL&apos;s rules.</>}
      </p>
    </div>
  );
}

/** What applying does to one host: what changes, what could affect its
 * connectivity, and the rules in force afterwards. */
function ApplyBody({ p, name }: { p: FwPlan; name: string }) {
  const a = p.applied;
  const rules = ruleNames(p.explain);
  const mgmt = p.explain.filter((r) => r.reason.kind === "management");
  const first = !a.enforced || a.drift === "missing";
  const denies = p.changes_added.filter((l) => l.startsWith("deny "));
  const outRestricted = p.explain.some((r) => r.direction === "out" && r.reason.kind === "default" && r.verdict === "drop");
  return (
    <div className="mt-2 space-y-2" data-testid="review-apply">
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
          <li data-testid="review-rollback-scope">Automatic rollback protects only DROOL&apos;s own connection{p.transport === "agent" ? " (here, the Shepherd's connection to the controller)" : ""}: while
            it keeps working, the change stays even where it blocks other access, such as your own SSH or a web interface. Give those a rule first.</li>
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
}
