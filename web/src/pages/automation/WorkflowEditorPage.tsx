import { useMemo, useState, type FormEvent } from "react";
import { Link, useLocation, useNavigate, useParams, useSearchParams } from "react-router";
import { useMutation, useQuery } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { useAuth } from "@/auth/AuthContext";
import {
  useInvalidateAutomation, useTemplates, useWorkflow, useWorkflowRevision,
  type Operation, type Strategy, type Workflow, type WorkflowTemplate,
} from "@/components/automation/queries";
import { RolloutAdvice, RolloutFields, WavesList, defaultRollout, mutates, recommendedRollout, rolloutFrom, rolloutText, strategyFrom, type RolloutState } from "@/components/automation/Rollout";
import { StepEditor, draftFromOp, draftProblem, draftToOp, type StepDraft } from "@/components/automation/StepEditor";
import { TargetPicker, hasTargets, type TargetSpec } from "@/components/automation/TargetPicker";
import { fmtTime } from "@/components/monitoring/format";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { Dialog } from "@/components/ui/Dialog";
import { Field, Input } from "@/components/ui/Field";
import { PageHeader } from "@/components/ui/PageHeader";
import { plainLink } from "@/components/ui/links";
import { useDebounced } from "@/hooks/useDebounced";
import { errorMessage } from "@/lib/format";
import { describeStep } from "@/pages/jobs/StepResult";
import { RunReview, StepSummary, hostCount } from "@/components/jobs/RunConfirmDialog";
import { confirmAction } from "@/components/ui/ConfirmDialog";

/** What "Save as workflow" on a new job hands over. */
export interface WorkflowDraft {
  name?: string;
  description?: string;
  steps?: Operation[];
  strategy?: Strategy;
}

export function WorkflowEditorPage() {
  const { id } = useParams();
  const wf = useWorkflow(id);
  const [params] = useSearchParams();
  const { state } = useLocation() as { state: WorkflowDraft | null };
  const templates = useTemplates();
  if (id) {
    if (wf.error) return <Alert>{errorMessage(wf.error)}</Alert>;
    if (!wf.data) return <p className="text-muted">Loading…</p>;
    return <WorkflowForm key={wf.data.latest_revision} existing={wf.data} draft={{}} />;
  }
  if (state) return <WorkflowForm draft={state} />;
  const key = params.get("template");
  if (!key) return <TemplateChooser templates={templates.data?.items ?? []} />;
  if (key === "blank") return <WorkflowForm draft={{}} />;
  const t = templates.data?.items.find((x) => x.key === key);
  if (!t) return <p className="text-muted">{templates.isPending ? "Loading…" : "Unknown template."}</p>;
  return <WorkflowForm draft={{ name: t.name, description: t.description, steps: t.steps, strategy: t.strategy }} />;
}

function TemplateChooser({ templates }: { templates: WorkflowTemplate[] }) {
  const [, setParams] = useSearchParams();
  return (
    <>
      <PageHeader crumbs={[{ label: "Workflows", to: "/jobs/workflows" }]} title="New workflow" description="Start from a recommended pattern, or from nothing. Templates are ordinary workflows once saved: change anything."
        actions={<Link to="/jobs/workflows" className="inline-flex h-9 items-center rounded-ui px-3.5 text-sm hover:bg-surface-2">Cancel</Link>} />
      <ul className="grid gap-4 md:grid-cols-2">
        {templates.map((t) => (
          <li key={t.key}>
            <Card title={t.name}>
              <p className="text-sm text-muted">{t.description}</p>
              <ol className="mt-2 list-decimal pl-5 text-sm">{t.steps.map((s, i) => <li key={i}>{describeStep(s)}</li>)}</ol>
              <p className="mt-2 text-xs text-muted">Rollout: {rolloutText(t.strategy)}.</p>
              <Button className="mt-3" onClick={() => setParams({ template: t.key })}>Use “{t.name}”</Button>
            </Card>
          </li>
        ))}
        <li>
          <Card title="Blank workflow">
            <p className="text-sm text-muted">Add your own steps: commands, scripts, packages, services, reboots, file deployments, and checks.</p>
            <Button className="mt-3" onClick={() => setParams({ template: "blank" })}>Start blank</Button>
          </Card>
        </li>
      </ul>
    </>
  );
}

function WorkflowForm({ existing, draft }: { existing?: Workflow; draft: WorkflowDraft }) {
  const navigate = useNavigate();
  const { can } = useAuth();
  const inv = useInvalidateAutomation();
  const [name, setName] = useState(existing?.name ?? draft.name ?? "");
  const [description, setDescription] = useState(existing?.description ?? draft.description ?? "");
  const [steps, setSteps] = useState<StepDraft[]>(() => (existing?.steps ?? draft.steps ?? []).map(draftFromOp));
  const [rollout, setRolloutState] = useState<RolloutState>(() => {
    // A workflow has no targets, so chosen canary hosts become a count.
    const r = rolloutFrom(existing?.strategy ?? draft.strategy);
    return r.canary === "hosts" ? { ...r, canary: "count", canaryCount: String(r.canaryHosts.length), canaryHosts: [] } : r;
  });
  // A saved workflow, or one that came with a rollout (a template, a run),
  // keeps the one it has; a new one that changes hosts gets the recommended
  // rollout until someone chooses another.
  const [rolloutChosen, setRolloutChosen] = useState(!!existing || !!draft.strategy);
  const [editRollout, setEditRollout] = useState(false);
  const setRollout = (v: RolloutState) => {
    setRolloutState(v);
    setRolloutChosen(true);
  };
  const [notice, setNotice] = useState("");
  const [running, setRunning] = useState(false);
  const [viewRev, setViewRev] = useState<number>();
  const editable = can("workflows.manage") && !existing?.archived;
  const problem = steps.map(draftProblem).find(Boolean);
  const ops = useMemo(() => steps.map(draftToOp), [steps]);
  const autoRollout = !rolloutChosen && mutates(ops);
  const effective = rolloutChosen ? rollout : autoRollout ? recommendedRollout : defaultRollout;
  const strategy = useMemo(() => strategyFrom(effective), [effective]);

  const save = useMutation({
    mutationFn: async () => {
      const body = { name: name.trim(), description: description.trim(), steps: ops, strategy };
      if (!existing) return { created: await unwrap(api.POST("/workflows", { body })) };
      return { updated: await unwrap(api.PUT("/workflows/{id}", { params: { path: { id: existing.id } }, body })) };
    },
    onSuccess: (r) => {
      inv();
      if (r.created) navigate(`/jobs/workflows/${r.created.id}`, { replace: true });
      else if (r.updated && !r.updated.changed) setNotice("Nothing changed, so no new revision was saved.");
    },
  });
  const archive = useMutation({
    mutationFn: () => unwrap(api.DELETE("/workflows/{id}", { params: { path: { id: existing!.id } } })),
    onSuccess: () => { inv(); navigate("/jobs/workflows"); },
  });

  function onSubmit(e: FormEvent) {
    e.preventDefault();
    setNotice("");
    if (name.trim() && steps.length && !problem) save.mutate();
  }

  return (
    <>
      <PageHeader
        crumbs={[{ label: "Workflows", to: "/jobs/workflows" }]}
        title={existing ? existing.name : "New workflow"}
        description={existing
          ? `Revision ${existing.latest_revision}${existing.archived ? " · archived" : ""}. Saving a change stores a new revision; schedules keep the revision they use until someone updates them.`
          : "An ordered list of steps. Each host runs them in order; a failed step stops that host's run."}
        actions={
          <>
            {existing && !existing.archived && can("jobs.execute") && <Button onClick={() => setRunning(true)}>Run now</Button>}
            {existing && !existing.archived && can("schedules.manage") && can("jobs.execute") && (
              <Link to={`/jobs/schedules/new?workflow=${existing.id}`} className="inline-flex h-9 items-center rounded-ui border border-line bg-surface px-3.5 text-sm font-medium hover:bg-surface-2">Schedule</Link>
            )}
            {!existing && <Link to="/jobs/workflows" className="inline-flex h-9 items-center rounded-ui px-3.5 text-sm hover:bg-surface-2">Cancel</Link>}
          </>
        }
      />
      {existing && <p className="-mt-3 mb-4 text-sm"><Link className={plainLink} to="/jobs/workflows">← Workflows</Link></p>}
      {existing && existing.outdated_schedules > 0 && (
        <p className="mb-4 rounded-ui border border-accent/40 bg-accent/10 px-3 py-2 text-sm" role="note">
          {existing.outdated_schedules} schedule{existing.outdated_schedules === 1 ? " runs" : "s run"} an older revision of this workflow and keep{existing.outdated_schedules === 1 ? "s" : ""} it until updated on {existing.outdated_schedules === 1 ? "its" : "their"} schedule page.
        </p>
      )}
      <div className="grid gap-4 xl:grid-cols-[1fr_22rem]">
        <form onSubmit={onSubmit} className="space-y-4">
          <Card title="Workflow">
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="Name"><Input value={name} maxLength={100} required disabled={!editable} autoFocus={!existing} onChange={(e) => setName(e.target.value)} /></Field>
              <Field label="Description" hint="Optional."><Input value={description} maxLength={1000} disabled={!editable} onChange={(e) => setDescription(e.target.value)} /></Field>
            </div>
          </Card>
          <Card title="Steps">
            {editable ? <StepEditor steps={steps} setSteps={setSteps} /> : (
              <ol className="list-decimal space-y-1 pl-5 text-sm">{ops.map((s, i) => <li key={i}>{describeStep(s)}</li>)}</ol>
            )}
            {editable && problem && <p className="mt-2 text-xs text-muted">To save: {problem}.</p>}
          </Card>
          <Card title="Default rollout">
            <p className="mb-3 text-sm text-muted">How it runs on several hosts. Running it or scheduling it starts from this rollout, which can be changed there.</p>
            {editable ? (
              <div className="space-y-3">
                <p className="text-sm">
                  <strong>{autoRollout ? "Recommended:" : "Rollout:"}</strong> {rolloutText(strategy)}.
                  {autoRollout && <span className="text-muted"> A bad change stops early instead of reaching every host.</span>}
                </p>
                <RolloutAdvice hosts={2} steps={ops} strategy={strategy} apply={() => setRollout(recommendedRollout)} />
                <button type="button" className="text-sm text-accent underline underline-offset-2" aria-expanded={editRollout}
                  onClick={() => {
                    if (!editRollout && !rolloutChosen) setRolloutState(effective); // start from what is shown
                    setEditRollout((v) => !v);
                  }}>
                  {editRollout ? "Hide rollout settings" : "Change rollout…"}
                </button>
                {editRollout && <RolloutFields v={rolloutChosen ? rollout : effective} set={setRollout} hosts={[]} noExplicit />}
              </div>
            ) : <p className="text-sm">{rolloutText(strategy)}.</p>}
          </Card>
          {save.error && <Alert>{errorMessage(save.error)}</Alert>}
          {notice && <p className="text-sm text-muted" role="status">{notice}</p>}
          {editable && (
            <div className="flex flex-wrap gap-2">
              <Button type="submit" variant="primary" disabled={!name.trim() || !steps.length || !!problem || save.isPending}>
                {save.isPending ? "Saving…" : existing ? "Save as a new revision" : "Create workflow"}
              </Button>
              {existing && (
                <Button type="button" variant="ghost" disabled={archive.isPending}
                  onClick={() => void confirmAction({ title: `Archive the workflow “${existing.name}”?`, confirmLabel: "Archive workflow",
                    body: <p>Schedules that use it keep running the revision they use; new schedules and runs can&apos;t choose it any more.</p> })
                    .then((ok) => ok && archive.mutate())}>
                  Archive
                </Button>
              )}
            </div>
          )}
          {archive.error && <Alert>{errorMessage(archive.error)}</Alert>}
        </form>
        {existing && (
          <Card title="Revisions">
            <ol className="space-y-2 text-sm">
              {(existing.revisions ?? []).map((r) => (
                <li key={r.revision} className="flex flex-wrap items-baseline gap-2">
                  <span className="font-medium">Revision {r.revision}</span>
                  <span className="text-muted">{r.steps} step{r.steps === 1 ? "" : "s"} · {r.created_by || "unknown"} · {fmtTime(r.created_at)}</span>
                  <Button size="sm" variant="ghost" onClick={() => setViewRev(r.revision)} aria-label={`View revision ${r.revision}`}>View</Button>
                </li>
              ))}
            </ol>
            <p className="mt-3 text-xs text-muted">Revisions never change. Past jobs keep exactly the steps they ran.</p>
          </Card>
        )}
      </div>
      {running && existing && <RunDialog wf={existing} onClose={() => setRunning(false)} />}
      {viewRev && existing && <RevisionDialog id={existing.id} rev={viewRev} onClose={() => setViewRev(undefined)} />}
    </>
  );
}

function RevisionDialog({ id, rev, onClose }: { id: string; rev: number; onClose: () => void }) {
  const q = useWorkflowRevision(id, rev);
  return (
    <Dialog title={`Revision ${rev}`} onClose={onClose}>
      {q.error && <Alert>{errorMessage(q.error)}</Alert>}
      {q.data && (
        <div className="space-y-3 text-sm">
          <p className="text-muted">{q.data.name} · saved by {q.data.created_by || "unknown"} · {fmtTime(q.data.created_at)}</p>
          <ol className="list-decimal space-y-1 pl-5">{q.data.steps.map((s, i) => <li key={i}>{describeStep(s)}</li>)}</ol>
          <p className="text-xs text-muted">Rollout: {rolloutText(q.data.strategy)}.</p>
        </div>
      )}
      <div className="mt-4 flex justify-end"><Button onClick={onClose} data-autofocus>Close</Button></div>
    </Dialog>
  );
}

/** Run the latest revision now, as an ordinary job: targets and rollout,
 * then the review (what, on which hosts, how it rolls out, what is in the
 * way) in the same dialog, then Start. */
function RunDialog({ wf, onClose }: { wf: Workflow; onClose: () => void }) {
  const navigate = useNavigate();
  const inv = useInvalidateAutomation();
  const [targets, setTargets] = useState<TargetSpec>({ hosts: [], groups: [], tags: [] });
  const [rollout, setRollout] = useState<RolloutState>(rolloutFrom(wf.strategy));
  const [reviewing, setReviewing] = useState(false);
  const strategy = useMemo(() => strategyFrom(rollout), [rollout]);
  const key = useDebounced({ targets, strategy }, 300);
  const preview = useQuery({
    queryKey: ["jobs", "preview", wf.id, key],
    queryFn: () => unwrap(api.POST("/jobs/preview", { body: { targets: key.targets, steps: wf.steps, strategy: key.strategy } })),
    enabled: hasTargets(key.targets),
    retry: false,
  });
  const [idem] = useState(() => crypto.randomUUID());
  const run = useMutation({
    mutationFn: () => unwrap(api.POST("/workflows/{id}/run", {
      params: { path: { id: wf.id }, header: { "Idempotency-Key": idem } }, body: { targets, strategy, revision: wf.latest_revision },
    })),
    onSuccess: (j) => { inv(); navigate(`/jobs/${j.id}`); },
  });
  const count = preview.data?.hosts.filter((h) => !h.skip).length ?? 0;
  // The review shows the preview of exactly these targets and this rollout.
  const current = JSON.stringify(key) === JSON.stringify({ targets, strategy }) && !preview.isFetching;
  const n = hostCount(count);
  return (
    <Dialog title={reviewing ? `Start “${wf.name}” on ${n}?` : `Run ${wf.name}`} onClose={onClose} wide testId="workflow-run"
      footer={reviewing ? (
        <>
          <Button onClick={() => setReviewing(false)} data-autofocus>Back</Button>
          <Button variant="primary" disabled={run.isPending} onClick={() => run.mutate()}>{run.isPending ? "Starting…" : `Start run on ${n}`}</Button>
        </>
      ) : (
        <>
          <Button variant="ghost" onClick={onClose}>Cancel</Button>
          <Button type="submit" form="workflow-run-form" variant="primary" disabled={!count || !current}>{count ? `Review: run on ${n}…` : "Choose targets"}</Button>
        </>
      )}>
      {reviewing && preview.data ? (
        <div className="space-y-3">
          <RunReview preview={preview.data} strategy={strategy}
            what={<>
              <span>Workflow <strong>{wf.name}</strong>, revision {wf.latest_revision}: {wf.steps.length} step{wf.steps.length === 1 ? "" : "s"}, in order on each host</span>
              <ol className="mt-1 list-decimal space-y-0.5 pl-5 text-xs">{wf.steps.map((st, i) => <li key={i}><StepSummary step={st} /></li>)}</ol>
            </>}
            consequences={["The run's page shows each host's result as it goes."]} />
          {run.error && <Alert>{errorMessage(run.error)}</Alert>}
        </div>
      ) : (
        <form id="workflow-run-form" className="space-y-4" onSubmit={(e) => {
          e.preventDefault();
          if (count && current) {
            run.reset();
            setReviewing(true);
          }
        }}>
          <div className="grid gap-4 md:grid-cols-2">
            <div>
              <h3 className="mb-2 text-sm font-semibold">Targets</h3>
              <TargetPicker v={targets} set={setTargets} />
            </div>
            <div className="space-y-4">
              <h3 className="text-sm font-semibold">Rollout</h3>
              <RolloutAdvice hosts={count} steps={wf.steps} strategy={strategy} apply={() => setRollout(recommendedRollout)} />
              <RolloutFields v={rollout} set={setRollout} hosts={(preview.data?.hosts ?? []).filter((h) => !h.skip).map((h) => ({ id: h.id, name: h.name }))} />
              {preview.error && <Alert>{errorMessage(preview.error)}</Alert>}
              {preview.data && (
                <div>
                  <h3 className="mb-1 text-sm font-semibold">Waves</h3>
                  <WavesList waves={preview.data.waves} />
                  {preview.data.warnings.map((w) => <p key={w} className="mt-1 text-xs text-muted">{w}</p>)}
                </div>
              )}
            </div>
          </div>
        </form>
      )}
    </Dialog>
  );
}
