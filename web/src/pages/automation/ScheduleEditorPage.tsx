import { useMemo, useState, type FormEvent } from "react";
import { Link, useLocation, useNavigate, useParams, useSearchParams } from "react-router";
import { useMutation, useQuery } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { useAuth } from "@/auth/AuthContext";
import {
  useInvalidateAutomation, useSchedule, useSchedules, useWorkflow, useWorkflowRevision, useWorkflows, minutesText,
  type Operation, type Schedule, type ScheduleInput, type SchedulePreview, type Strategy,
} from "@/components/automation/queries";
import { NextRunsList, RecurrenceFields, browserZone, defaultRecur, recurFrom, recurrenceFrom, type RecurState } from "@/components/automation/RecurrenceFields";
import { RolloutAdvice, RolloutFields, WavesList, defaultRollout, recommendedRollout, rolloutFrom, rolloutText, strategyFrom, type RolloutState } from "@/components/automation/Rollout";
import { StepEditor, draftFromOp, draftProblem, draftToOp, type StepDraft } from "@/components/automation/StepEditor";
import { TargetPicker, hasTargets, type TargetSpec } from "@/components/automation/TargetPicker";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { Checkbox } from "@/components/ui/Checkbox";
import { Field, Input, Select } from "@/components/ui/Field";
import { PageHeader } from "@/components/ui/PageHeader";
import { plainLink } from "@/components/ui/links";
import { useDebounced } from "@/hooks/useDebounced";
import { errorMessage } from "@/lib/format";
import { SimHostRow } from "@/pages/jobs/OperationForms";

/** What "Schedule this job" and "Schedule" on a workflow hand over. */
export interface ScheduleDraft {
  kind?: "job" | "maintenance";
  name?: string;
  steps?: Operation[];
  targets?: TargetSpec;
  strategy?: Strategy;
  workflowId?: string;
}

/** New schedule, or edit one (future runs only). */
export function ScheduleEditorPage() {
  const { id } = useParams();
  const existing = useSchedule(id);
  const [params] = useSearchParams();
  const { state } = useLocation() as { state: ScheduleDraft | null };
  const wfParam = params.get("workflow") ?? undefined;
  const wf = useWorkflow(!id ? wfParam : undefined);
  if (id) {
    if (existing.error) return <Alert>{errorMessage(existing.error)}</Alert>;
    if (!existing.data) return <p className="text-muted">Loading…</p>;
    return <ScheduleForm existing={existing.data} draft={{}} />;
  }
  const draft: ScheduleDraft = { ...state };
  if (params.get("kind") === "maintenance") draft.kind = "maintenance";
  if (wfParam) {
    // Start from the workflow's own rollout.
    if (wf.error) return <Alert>{errorMessage(wf.error)}</Alert>;
    if (!wf.data) return <p className="text-muted">Loading…</p>;
    draft.workflowId = wfParam;
    draft.strategy = wf.data.strategy;
    draft.name ??= wf.data.name;
  }
  return <ScheduleForm draft={draft} />;
}

const durations = [["60", "1 hour"], ["120", "2 hours"], ["240", "4 hours"], ["480", "8 hours"], ["1440", "1 day"]] as const;

function ScheduleForm({ existing, draft }: { existing?: Schedule; draft: ScheduleDraft }) {
  const navigate = useNavigate();
  const { me } = useAuth();
  const inv = useInvalidateAutomation();
  const c = existing?.config;
  const kind = existing?.kind ?? draft.kind ?? "job";
  const maint = kind === "maintenance";

  const [name, setName] = useState(existing?.name ?? draft.name ?? "");
  const [zone, setZone] = useState(c?.time_zone ?? browserZone());
  const [recur, setRecur] = useState<RecurState>(() => (c ? recurFrom(c.recurrence, c.time_zone) : defaultRecur(zone)));
  const [targets, setTargets] = useState<TargetSpec>(c?.targets ?? draft.targets ?? { hosts: [], groups: [], tags: [] });
  const [mode, setMode] = useState<"steps" | "workflow">(c?.workflow || draft.workflowId ? "workflow" : "steps");
  const [steps, setSteps] = useState<StepDraft[]>(() => (c?.steps ?? draft.steps ?? []).map(draftFromOp));
  const [wfId, setWfId] = useState(c?.workflow?.id ?? draft.workflowId ?? "");
  const [wfRev, setWfRev] = useState(c?.workflow?.revision ?? 0); // 0: the latest when saved
  const [rollout, setRollout] = useState<RolloutState>(() => (c ? rolloutFrom(c.strategy) : draft.strategy ? rolloutFrom(draft.strategy) : defaultRollout));
  const [missed, setMissed] = useState<"skip" | "run_once">(c?.missed_policy === "run_once" ? "run_once" : "skip");
  const [late, setLate] = useState(c?.late_limit_minutes ? String(c.late_limit_minutes) : "");
  const [overlap, setOverlap] = useState<"skip" | "queue_one">(c?.overlap_policy === "queue_one" ? "queue_one" : "skip");
  const [windowId, setWindowId] = useState(c?.window_id ?? "");
  const [duration, setDuration] = useState(String(c?.duration_minutes ?? 120));
  const [reason, setReason] = useState(c?.reason ?? "");
  const [enabled, setEnabled] = useState(existing?.enabled ?? true);
  const [advanced, setAdvanced] = useState(false);

  const workflows = useWorkflows(false, !maint);
  const wf = useWorkflow(mode === "workflow" && wfId ? wfId : undefined);
  const pinned = useWorkflowRevision(wfId || undefined, wfRev && wf.data && wfRev !== wf.data.latest_revision ? wfRev : undefined);
  const windows = useSchedules("maintenance", false, !maint);

  const recurrence = useMemo(() => recurrenceFrom(recur), [recur]);
  const strategy = useMemo(() => strategyFrom(rollout), [rollout]);
  const ops = useMemo(() => steps.map(draftToOp), [steps]);
  const runSteps = mode === "workflow" ? (pinned.data?.steps ?? wf.data?.steps ?? []) : ops;
  const stepProblem = steps.map(draftProblem).find(Boolean);
  const actionReady = maint || (mode === "workflow" ? !!wfId : steps.length > 0 && !stepProblem);

  const input: ScheduleInput = useMemo(() => {
    const base: ScheduleInput = { kind, name: name.trim() || "Untitled", recurrence, time_zone: zone, targets };
    if (maint) return { ...base, duration_minutes: Number(duration) || 0, reason };
    return {
      ...base,
      ...(mode === "workflow" ? { workflow: { id: wfId, revision: wfRev || undefined } } : { steps: ops }),
      strategy,
      missed_policy: missed,
      late_limit_minutes: late ? Number(late) : 0,
      overlap_policy: overlap,
      window_id: windowId || undefined,
    };
  }, [kind, name, recurrence, zone, targets, maint, duration, reason, mode, wfId, wfRev, ops, strategy, missed, late, overlap, windowId]);

  const debounced = useDebounced(input);
  const preview = useQuery({
    queryKey: ["schedules", "preview", debounced],
    queryFn: () => unwrap(api.POST("/schedules/preview", { body: debounced })),
    enabled: hasTargets(debounced.targets) && actionReady,
    retry: false,
  });

  const save = useMutation({
    mutationFn: () => {
      const body = { ...input, name: name.trim(), enabled: existing ? undefined : enabled };
      return existing
        ? unwrap(api.PUT("/schedules/{id}", { params: { path: { id: existing.id } }, body }))
        : unwrap(api.POST("/schedules", { body }));
    },
    onSuccess: (s) => {
      inv();
      navigate(`/jobs/schedules/${s.id}`);
    },
  });

  const chooseWorkflow = (id: string) => {
    setWfId(id);
    setWfRev(0);
    // A workflow brings its recommended rollout; it can be changed below.
    const w = workflows.data?.items.find((x) => x.id === id);
    if (w) setRollout(rolloutFrom(w.strategy));
  };

  const eligible = (preview.data?.targets ?? []).filter((t) => t.wave >= 0);
  const recurring = recur.mode !== "once";
  const notOwner = existing && existing.owner?.id !== me?.user.id;
  const ready = name.trim() !== "" && hasTargets(targets) && actionReady && !save.isPending;
  const back = existing ? `/jobs/schedules/${existing.id}` : maint ? "/alerts?view=maintenance" : "/jobs/schedules";

  function onSubmit(e: FormEvent) {
    e.preventDefault();
    if (ready) save.mutate();
  }

  return (
    <>
      <PageHeader
        title={existing ? `Edit ${existing.name}` : maint ? "New recurring maintenance window" : "New schedule"}
        description={existing
          ? "Changes apply to future runs only. A run in progress and earlier runs keep exactly what they ran."
          : maint
            ? "Pauses notifications for these hosts at set times, like starting maintenance by hand."
            : "Run a job or workflow at set times. Every run is an ordinary job under Runs."}
        actions={<Link to={back} className="inline-flex h-9 items-center rounded-ui px-3.5 text-sm hover:bg-surface-2">Cancel</Link>}
      />
      <form onSubmit={onSubmit} className="grid gap-4 xl:grid-cols-[1fr_26rem]">
        <div className="space-y-4">
          <Card title="Name">
            <div className="space-y-3">
              <Field label="Name" hint={maint ? "e.g. “Sunday patch window”." : "e.g. “Weekly updates”."}>
                <Input value={name} maxLength={100} required autoFocus={!existing} onChange={(e) => setName(e.target.value)} />
              </Field>
              {!existing && <Checkbox checked={enabled} onChange={setEnabled} label="Enabled" hint="A disabled schedule keeps its settings and history but doesn't run." />}
            </div>
          </Card>

          <Card title="When">
            <div className="space-y-4">
              <RecurrenceFields v={recur} set={setRecur} zone={zone} setZone={setZone} />
              <div className="rounded-ui border border-line p-3"><NextRunsList recurrence={recurrence} zone={zone} /></div>
            </div>
          </Card>

          <Card title="Targets">
            <TargetPicker v={targets} set={setTargets} noTags={maint}
              groupsHint="Groups are checked again at every run: hosts added to a group later take part." />
          </Card>

          {maint ? (
            <Card title="Window">
              <div className="space-y-4">
                <fieldset>
                  <legend className="mb-1.5 text-sm font-medium">How long</legend>
                  <div className="flex flex-wrap gap-2">
                    {durations.map(([k, label]) => (
                      <label key={k} className={`cursor-pointer rounded-ui border px-3 py-1.5 text-sm has-[:focus-visible]:ring-2 has-[:focus-visible]:ring-accent ${duration === k ? "border-accent bg-accent/15 font-medium" : "border-line"}`}>
                        <input type="radio" name="duration" className="sr-only" checked={duration === k} onChange={() => setDuration(k)} />
                        {label}
                      </label>
                    ))}
                  </div>
                  <div className="mt-2 max-w-[12rem]">
                    <Field label="Minutes" hint={`${minutesText(Number(duration) || 0)}. At most 7 days.`}>
                      <Input type="number" min={1} max={10080} value={duration} onChange={(e) => setDuration(e.target.value)} />
                    </Field>
                  </div>
                </fieldset>
                <Field label="Reason (recommended)" hint="Shown on suppressed alerts, e.g. “Weekly patching”.">
                  <Input value={reason} maxLength={200} onChange={(e) => setReason(e.target.value)} />
                </Field>
              </div>
            </Card>
          ) : (
            <Card title="What to run">
              <div className="space-y-4">
                <div className="flex flex-wrap gap-4 text-sm">
                  <label className="flex items-center gap-2"><input type="radio" name="what" checked={mode === "steps"} onChange={() => setMode("steps")} /> Steps</label>
                  <label className="flex items-center gap-2"><input type="radio" name="what" checked={mode === "workflow"} onChange={() => setMode("workflow")} /> A saved workflow</label>
                </div>
                {mode === "steps" ? (
                  <StepEditor steps={steps} setSteps={setSteps} />
                ) : (
                  <WorkflowChoice workflows={workflows.data?.items ?? []} id={wfId} rev={wfRev} setId={chooseWorkflow} setRev={setWfRev}
                    latest={wf.data?.latest_revision} summary={pinned.data ? undefined : wf.data?.summary} pinnedSteps={pinned.data?.steps} />
                )}
                {stepProblem && mode === "steps" && <p className="text-xs text-muted">To save: {stepProblem}.</p>}
              </div>
            </Card>
          )}

          {!maint && (
            <>
              <RolloutAdvice hosts={eligible.length} steps={runSteps} strategy={strategy} apply={() => setRollout(recommendedRollout)} />
              <Card title="Advanced"
                actions={<Button type="button" size="sm" variant="ghost" aria-expanded={advanced} onClick={() => setAdvanced(!advanced)}>{advanced ? "Hide" : "Show"}</Button>}>
                {!advanced ? (
                  <ul className="space-y-1 text-sm text-muted">
                    <li>Rollout: {rolloutText(strategy)}.</li>
                    <li>{recurring ? (missed === "skip" ? "Runs missed while DROOL is down are skipped." : "One missed run is made up when DROOL returns.")
                      : late ? `Not started if DROOL returns more than ${late} minutes late.` : "Starts when DROOL returns if it was down at the time."}</li>
                    <li>{overlap === "skip" ? "Skipped while the previous run is still going." : "One run waits while the previous one is still going."}</li>
                    <li>{windowId ? `Runs inside maintenance window “${windows.data?.items.find((w) => w.id === windowId)?.name ?? windowId}”.` : "Not tied to a maintenance window: hosts under maintenance are skipped."}</li>
                  </ul>
                ) : (
                  <div className="space-y-6">
                    <section aria-label="Rollout">
                      <h3 className="mb-2 text-sm font-semibold">Rollout</h3>
                      <RolloutFields v={rollout} set={setRollout} hosts={eligible.map((t) => ({ id: t.id, name: t.name }))} />
                    </section>
                    <section aria-label="Missed runs">
                      <h3 className="mb-2 text-sm font-semibold">If DROOL isn't running at the scheduled time</h3>
                      {recurring ? (
                        <div className="space-y-1.5 text-sm">
                          <label className="flex items-start gap-2"><input type="radio" name="missed" className="mt-1" checked={missed === "skip"} onChange={() => setMissed("skip")} />
                            <span>Skip it and continue with the next run <span className="text-muted">(recommended)</span></span></label>
                          <label className="flex items-start gap-2"><input type="radio" name="missed" className="mt-1" checked={missed === "run_once"} onChange={() => setMissed("run_once")} />
                            <span>Run one missed occurrence when DROOL returns<span className="block text-xs text-muted">Only the most recent one, never a backlog.</span></span></label>
                        </div>
                      ) : (
                        <Field label="Don't start if more than this many minutes late" hint="Empty: start when DROOL returns, however late.">
                          <Input type="number" min={1} max={10080} className="max-w-[10rem]" value={late} onChange={(e) => setLate(e.target.value)} />
                        </Field>
                      )}
                    </section>
                    <section aria-label="Overlap">
                      <h3 className="mb-2 text-sm font-semibold">If the previous run is still going</h3>
                      <div className="space-y-1.5 text-sm">
                        <label className="flex items-start gap-2"><input type="radio" name="overlap" className="mt-1" checked={overlap === "skip"} onChange={() => setOverlap("skip")} />
                          <span>Skip the new run <span className="text-muted">(recommended)</span></span></label>
                        <label className="flex items-start gap-2"><input type="radio" name="overlap" className="mt-1" checked={overlap === "queue_one"} onChange={() => setOverlap("queue_one")} />
                          <span>Queue one run to start when it finishes<span className="block text-xs text-muted">At most one waits; further runs are skipped.</span></span></label>
                      </div>
                    </section>
                    <section aria-label="Maintenance window">
                      <h3 className="mb-2 text-sm font-semibold">Maintenance window</h3>
                      <Field label="Run inside" hint="Scheduled runs skip hosts under maintenance. Tied to a recurring window, the run happens only while that window is active and may work on the hosts it covers (other maintenance still applies).">
                        <Select value={windowId} onChange={(e) => setWindowId(e.target.value)}>
                          <option value="">No window</option>
                          {(windows.data?.items ?? []).map((w) => <option key={w.id} value={w.id}>{w.name} ({w.when})</option>)}
                        </Select>
                      </Field>
                    </section>
                  </div>
                )}
              </Card>
            </>
          )}

          {notOwner && (
            <p className="rounded-ui border border-line bg-surface px-3 py-2 text-sm" role="note">
              Saving makes you this schedule's owner: future runs use your permissions{existing?.owner ? ` instead of ${existing.owner.name}'s` : ""}.
            </p>
          )}
          {save.error && <Alert>{errorMessage(save.error)}</Alert>}
          <Button type="submit" variant="primary" disabled={!ready}>{save.isPending ? "Saving…" : existing ? "Save changes" : "Create schedule"}</Button>
        </div>

        <div className="space-y-4 xl:sticky xl:top-4 xl:self-start">
          <PreviewPanel p={preview.data} error={preview.error} ready={hasTargets(targets) && actionReady}
            loading={preview.isFetching || JSON.stringify(input) !== JSON.stringify(debounced)} />
          {!maint && hasTargets(targets) && <PackageCheck targets={targets} steps={runSteps} />}
        </div>
      </form>
    </>
  );
}

function WorkflowChoice({ workflows, id, rev, setId, setRev, latest, summary, pinnedSteps }: {
  workflows: { id: string; name: string; latest_revision: number }[];
  id: string;
  rev: number;
  setId: (id: string) => void;
  setRev: (r: number) => void;
  latest?: number;
  summary?: string[];
  pinnedSteps?: Operation[];
}) {
  const outdated = !!latest && rev > 0 && rev < latest;
  return (
    <div className="space-y-3">
      <Field label="Workflow">
        <Select value={id} onChange={(e) => setId(e.target.value)}>
          <option value="">Choose…</option>
          {workflows.map((w) => <option key={w.id} value={w.id}>{w.name}</option>)}
        </Select>
      </Field>
      {workflows.length === 0 && <p className="text-xs text-muted">No workflows yet. <Link className="text-accent underline" to="/jobs/workflows/new">Create one</Link>, or schedule steps directly.</p>}
      {id && latest && (
        <p className="text-xs text-muted">
          Runs revision {rev || latest} exactly as it is now. Later edits to the workflow don't change this schedule until you choose to use them.
        </p>
      )}
      {outdated && (
        <div className="flex flex-wrap items-center gap-3 rounded-ui border border-accent/40 bg-accent/10 p-3 text-sm" role="note">
          <span className="min-w-0 flex-1">A newer workflow revision is available (revision {latest}; this schedule runs revision {rev}).
            {" "}<Link className={plainLink} to={`/jobs/workflows/${id}`}>Review the changes</Link>.</span>
          <Button type="button" size="sm" onClick={() => setRev(latest)}>Use revision {latest}</Button>
        </div>
      )}
      {pinnedSteps && <p className="text-xs text-muted">Revision {rev}: {pinnedSteps.length} step{pinnedSteps.length === 1 ? "" : "s"}.</p>}
      {summary && summary.length > 0 && (
        <ol className="list-decimal space-y-0.5 pl-5 text-sm">{summary.map((s, i) => <li key={i}>{s}</li>)}</ol>
      )}
    </div>
  );
}

/** What the schedule would do: next runs, today's targets and waves, steps,
 * reboots, checks, the window relationship and the policies. */
export function PreviewPanel({ p, error, loading, ready }: { p?: SchedulePreview; error: unknown; loading: boolean; ready: boolean }) {
  if (!ready) return <Card title="Preview"><p className="text-sm text-muted">Choose targets and what to run to see a preview.</p></Card>;
  if (error) return <Card title="Preview"><Alert>{errorMessage(error)}</Alert></Card>;
  if (!p) return <Card title="Preview"><p className="text-sm text-muted">{loading ? "Working it out…" : "…"}</p></Card>;
  const running = p.targets.filter((t) => t.wave >= 0);
  const skipped = p.targets.filter((t) => t.wave < 0);
  return (
    <Card title="Preview" actions={<span className="text-xs text-muted" aria-live="polite">{loading ? "Updating…" : ""}</span>}>
      <div className="space-y-4 text-sm" aria-busy={loading}>
        <section>
          <h3 className="font-semibold">When</h3>
          <p>{p.when}</p>
          <ul className="mt-1 space-y-0.5 text-muted">
            {p.next_runs.slice(0, 3).map((r) => <li key={r.at}>{r.local}{r.note && <span className="block text-xs text-warn">{r.note}</span>}</li>)}
          </ul>
        </section>
        <section>
          <h3 className="font-semibold">Targets now: {p.kind === "maintenance" ? p.targets.length : running.length} host{(p.kind === "maintenance" ? p.targets.length : running.length) === 1 ? "" : "s"}</h3>
          {p.kind === "maintenance" ? (
            <p className="text-muted">{p.targets.map((t) => t.name).join(", ") || "None"}</p>
          ) : (
            <>
              <WavesList waves={p.waves} />
              {skipped.length > 0 && (
                <ul className="mt-1 text-xs text-muted">
                  {skipped.map((t) => <li key={t.id}>{t.name}: skipped, in maintenance ({t.maintenance})</li>)}
                </ul>
              )}
            </>
          )}
        </section>
        {p.kind !== "maintenance" && (
          <>
            <section>
              <h3 className="font-semibold">Steps</h3>
              <ol className="list-decimal pl-5">{p.steps.map((s, i) => <li key={i}>{s}</li>)}</ol>
            </section>
            <section>
              <h3 className="font-semibold">Reboots</h3>
              <p>{p.reboot}</p>
            </section>
            <section>
              <h3 className="font-semibold">Verification</h3>
              {p.verification.length ? <ul className="list-disc pl-5">{p.verification.map((v) => <li key={v}>{v}</li>)}</ul> : <p className="text-muted">None. Add a verify step to check hosts afterwards.</p>}
            </section>
            <section>
              <h3 className="font-semibold">Rollout</h3>
              <p>{p.concurrency} at a time{p.failure_threshold ? `; pauses after ${p.failure_threshold} failure${p.failure_threshold === 1 ? "" : "s"}` : "; no failure limit"}.</p>
            </section>
          </>
        )}
        {p.duration && <section><h3 className="font-semibold">Each window lasts</h3><p>{p.duration}</p></section>}
        {p.window && <section><h3 className="font-semibold">Maintenance window</h3><p className={p.window.inside ? undefined : "text-warn"}>{p.window.note}</p></section>}
        {p.policies.length > 0 && <section><h3 className="font-semibold">Policies</h3><ul className="list-disc pl-5">{p.policies.map((x) => <li key={x}>{x}</li>)}</ul></section>}
        {p.warnings.map((w) => <p key={w} className="text-warn">{w}</p>)}
        {p.notes.map((n) => <p key={n} className="text-xs text-muted">{n}</p>)}
      </div>
    </Card>
  );
}

/** The package dry run of New job, for a schedule's first package or
 * system-update step: what would change if it ran now. Read-only: it never
 * changes the step's own "allow removals". */
function PackageCheck({ targets, steps }: { targets: TargetSpec; steps: Operation[] }) {
  const step = steps.find((s) => s.type === "package" || s.type === "system_update");
  const sim = useMutation({
    mutationFn: () => unwrap(api.POST("/jobs/simulate", { body: { targets, step: step! } })),
  });
  const key = JSON.stringify([targets, step]);
  const [ranFor, setRanFor] = useState("");
  if (!step) return null;
  const current = sim.data && ranFor === key;
  const removals = current ? sim.data!.hosts.filter((h) => h.extra_removals.length > 0).length : 0;
  return (
    <Card title="Package changes">
      <div className="space-y-3 text-sm">
        <div className="flex flex-wrap items-center gap-3">
          <Button type="button" size="sm" disabled={sim.isPending} onClick={() => { setRanFor(key); sim.mutate(); }}>
            {sim.isPending ? "Checking hosts…" : current ? "Check again" : "Check package changes now"}
          </Button>
          <span className="text-xs text-muted">Dry run on each host. Nothing is changed.</span>
        </div>
        <p className="text-xs text-muted">This shows what would change today. Repositories change, so a later run may install more (or different) updates.</p>
        {sim.error && <Alert>{errorMessage(sim.error)}</Alert>}
        {current && removals > 0 && (
          <Alert tone="warn">
            {removals} host{removals === 1 ? "" : "s"} would remove other packages. Unless the step allows removals, the run stops on {removals === 1 ? "that host" : "those hosts"} before changing anything.
          </Alert>
        )}
        {current && (
          <ul className="divide-y divide-line rounded-ui border border-line" aria-label="Package changes per host">
            {sim.data!.hosts.map((h) => <SimHostRow key={h.host_id} h={h} />)}
          </ul>
        )}
      </div>
    </Card>
  );
}
