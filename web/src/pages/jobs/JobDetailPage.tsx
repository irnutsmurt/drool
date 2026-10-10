import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { jk, useJob, useJobHost, useJobHosts, type JobHost, type JobSpec } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { useJobEvents } from "@/hooks/useJobEvents";
import { HostStateBadge, JobStatusBadge, errorExplanations } from "@/components/jobs/badges";
import { rolloutText } from "@/components/automation/Rollout";
import { Badge } from "@/components/ui/Badge";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { PageHeader } from "@/components/ui/PageHeader";
import { Table } from "@/components/ui/Table";
import { cx } from "@/components/ui/cx";
import { plainLink } from "@/components/ui/links";
import { errorMessage, formatDateTime } from "@/lib/format";
import { fmtTime } from "@/components/monitoring/format";
import { OutputPanel } from "./OutputPanel";
import { StepResult, describeStep } from "./StepResult";
import { summarizeCounts } from "./summary";

const activeStates = new Set(["queued", "waiting_lock", "connecting", "running", "rebooting", "waiting_host", "verifying"]);

function duration(h: JobHost): string {
  if (!h.started_at) return "—";
  const end = h.finished_at ? new Date(h.finished_at) : new Date();
  const s = Math.max(0, Math.round((end.getTime() - new Date(h.started_at).getTime()) / 1000));
  return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m ${s % 60}s`;
}

export function JobDetailPage() {
  const { id = "" } = useParams();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const { can } = useAuth();
  const job = useJob(id);
  const hosts = useJobHosts(id);
  const active = job.data ? ["pending", "running", "paused"].includes(job.data.status) : false;
  const events = useJobEvents(id, active);
  const [selected, setSelected] = useState<string>();

  const action = useMutation({
    mutationFn: async (kind: "cancel" | "resume" | "rerun") => {
      if (kind === "rerun") return unwrap(api.POST("/jobs/{id}/rerun", { params: { path: { id } }, body: { hosts: "failed" } }));
      await unwrap(kind === "cancel"
        ? api.POST("/jobs/{id}/cancel", { params: { path: { id } } })
        : api.POST("/jobs/{id}/resume", { params: { path: { id } } }));
      return undefined;
    },
    onSuccess: (res) => {
      void qc.invalidateQueries({ queryKey: jk.job(id) });
      if (res && "id" in res) navigate(`/jobs/${res.id}`);
    },
  });

  if (job.error) return <><PageHeader crumbs={[{ label: "Runs", to: "/jobs" }]} title="Run" /><Alert>{errorMessage(job.error)}</Alert></>;
  const j = job.data;
  if (!j) return <p className="text-muted">Loading…</p>;
  const items = hosts.data?.items ?? [];
  // Show something useful without a click: the chosen host, else the first
  // host with a problem, else the first host.
  const problem = (h: JobHost) => ["failed", "timed_out", "interrupted", "cancelled"].includes(h.state);
  const current = items.find((h) => h.host_id === selected) ?? items.find(problem) ?? items[0];
  const failedCount = (j.counts.failed ?? 0) + (j.counts.timed_out ?? 0) + (j.counts.cancelled ?? 0) +
    (j.counts.skipped ?? 0) + (j.counts.interrupted ?? 0);
  const steps = j.spec.steps;
  const strategy = j.spec.strategy ?? { concurrency: 10 };
  const o = j.origin;
  const waves = waveGroups(items, strategy);
  const outside = items.filter((h) => h.wave < 0);
  // Hosts that run only some of the steps (a desired-state correction).
  const perHost = items.some((h) => h.steps != null);

  return (
    <>
      <PageHeader
        crumbs={[{ label: "Runs", to: "/jobs" }]}
        title={j.name}
        description={
          <span className="inline-flex flex-wrap items-center gap-2">
            <JobStatusBadge job={j} /> {summarizeCounts(j)} · started {formatDateTime(j.created_at)} by {j.created_by}
            {j.rerun_of && <>· <Link className={plainLink} to={`/jobs/${j.rerun_of}`}>re-run of earlier job</Link></>}
          </span>
        }
        actions={can("jobs.execute") && (
          <>
            {j.status === "paused" && <Button variant="primary" onClick={() => action.mutate("resume")}>Resume</Button>}
            {active && (
              <Button variant="danger" disabled={action.isPending}
                onClick={() => {
                  const q = o?.schedule_id
                    ? `Cancel this run? Running commands will be terminated.\nThe schedule “${o.schedule_name ?? ""}” stays enabled; its next run happens as planned.`
                    : "Cancel this run? Running commands will be terminated.";
                  if (confirm(q)) action.mutate("cancel");
                }}>
                Cancel
              </Button>
            )}
            {!active && failedCount > 0 && !o?.reconcile_id && (
              <Button onClick={() => action.mutate("rerun")} disabled={action.isPending}>Re-run unsuccessful hosts</Button>
            )}
          </>
        )}
      />
      {o?.reconcile_id && (
        <p className="-mt-3 mb-4 text-sm text-muted">
          {o.reconcile_kind === "auto"
            ? <>Automatic restore of baseline <Link className={plainLink} to={`/desired/baselines/${o.baseline_id}`}>{o.baseline_name || o.baseline_id}</Link>, run with its owner {j.created_by}'s permissions. </>
            : <>Restore managed state, started by {j.created_by}. </>}
          Each host runs only its own corrections; afterwards DROOL checks every host again and records what it finds on its{" "}
          <Link className={plainLink} to="/desired">desired-state</Link> page.
        </p>
      )}
      {o && (o.schedule_id || o.workflow_id) && (
        <p className="-mt-3 mb-4 text-sm text-muted">
          {o.schedule_id && (
            <>
              {o.manual ? "Started with Run now from schedule " : "Scheduled run of "}
              <Link className={plainLink} to={`/jobs/schedules/${o.schedule_id}`}>{o.schedule_name || "a schedule"}</Link>
              {!o.manual && o.scheduled_for ? ` for ${fmtTime(o.scheduled_for)}` : ""}
              {!o.manual && `, with ${j.created_by}'s permissions`}
              {o.workflow_id ? " · " : ""}
            </>
          )}
          {o.workflow_id && (
            <>workflow <Link className={plainLink} to={`/jobs/workflows/${o.workflow_id}`}>{o.workflow_name || o.workflow_id}</Link>, revision {o.workflow_revision}</>
          )}
        </p>
      )}
      {j.status === "paused" && (
        <div className="mb-4">
          <Alert tone="warn">
            Paused: {j.pause_reason}{/resume/i.test(j.pause_reason ?? "") ? "." : ". Fix the cause, then resume or cancel."}
            {" "}Hosts that failed stay failed when you resume.
          </Alert>
        </div>
      )}
      {j.status === "interrupted" && (
        <div className="mb-4"><Alert tone="warn">The controller restarted while this run was in progress. Hosts that were in progress have an unknown outcome{o?.schedule_id ? "; the schedule doesn't re-run it automatically" : ""}.</Alert></div>
      )}
      {active && o?.schedule_id && (
        <p className="mb-4 rounded-ui border border-line bg-surface px-3 py-2 text-sm" role="note">
          Cancelling stops this run only: the schedule stays enabled. Disabling the schedule stops future runs but doesn't stop this one.
        </p>
      )}
      {action.error && <div className="mb-4"><Alert>{errorMessage(action.error)}</Alert></div>}

      <div className="grid gap-4">
        <Card title={steps.length > 1 ? `What ran (${steps.length} steps, in order on each host)` : "What ran"}>
          <ol className="space-y-3">
            {steps.map((st, i) => (
              <li key={i} className={steps.length > 1 ? "border-l-2 border-line pl-3" : undefined}>
                {steps.length > 1 && (
                  <div className="mb-1 text-xs font-medium uppercase text-muted">
                    Step {i + 1}{perHost && <span className="normal-case"> — on {items.filter((h) => !h.steps || h.steps.includes(i)).length} of {items.length} hosts</span>}
                  </div>
                )}
                <StepText step={st} />
              </li>
            ))}
          </ol>
          <p className="mt-3 text-xs text-muted">Rollout: {rolloutText(strategy)}.</p>
        </Card>

        {waves.length > 1 && (
          <Card title="Rollout progress">
            <ol className="space-y-1.5 text-sm" aria-label="Rollout waves">
              {waves.map((w) => (
                <li key={w.wave} className="flex flex-wrap items-baseline gap-2">
                  <span className="w-20 shrink-0 font-medium">{w.label}</span>
                  <span className="min-w-0 flex-1 text-muted">{w.names}</span>
                  <span className={w.failed ? "text-danger" : w.done ? "text-ok" : "text-muted"}>{w.status}</span>
                </li>
              ))}
            </ol>
            {outside.length > 0 && <p className="mt-2 text-xs text-muted">{outside.length} host{outside.length === 1 ? " was" : "s were"} not part of this run (skipped before it started).</p>}
          </Card>
        )}

        <Card title={items.length > 1 ? "Hosts (click one to see its output)" : "Hosts"}>
          <Table<JobHost>
            rows={items}
            rowKey={(h) => h.host_id}
            empty={hosts.isPending ? "Loading…" : "No hosts."}
            columns={[
              {
                header: "Host",
                cell: (h) => (
                  <button type="button" onClick={() => setSelected(h.host_id)}
                    className={cx("text-left font-medium", current?.host_id === h.host_id ? "text-fg underline" : "text-accent")}>
                    {h.host_name}
                  </button>
                ),
              },
              { header: "State", cell: (h) => <HostStateBadge state={h.state} /> },
              ...(waves.length > 1 ? [{ header: "Wave", cell: (h: JobHost) => <span className="text-muted">{h.wave < 0 ? "—" : waves.find((w) => w.wave === h.wave)?.label}</span> }] : []),
              { header: "Exit", cell: (h) => (h.exit_code ?? "—") },
              { header: "Via", cell: (h) => h.transport ? <span title={h.transport_note}><Badge tone={h.transport_note ? "warn" : h.transport === "agent" ? "ok" : "neutral"}>{h.transport === "agent" ? "agent" : h.transport_note ? "SSH fallback" : "SSH"}</Badge></span> : "" },
              { header: "Time", cell: (h) => <span className="text-muted">{duration(h)}</span> },
              { header: "Problem", cell: (h) => <span className="text-muted">{h.error_message}</span> },
            ]}
          />
        </Card>

        {current && (
          <HostOutput jobId={id} host={current} live={active && activeStates.has(current.state)} onOutput={events.onOutput} />
        )}
      </div>
    </>
  );
}

function HostOutput({ jobId, host, live, onOutput }: {
  jobId: string;
  host: JobHost;
  live: boolean;
  onOutput: Parameters<typeof OutputPanel>[0]["onOutput"];
}) {
  const detail = useJobHost(jobId, host.host_id);
  const steps = detail.data?.steps ?? [];
  return (
    <Card title={`Output: ${host.host_name}`}>
      {host.transport_note && (
        <div className="mb-3"><Alert tone="warn">{host.transport_note}</Alert></div>
      )}
      {host.steps && (
        <p className="mb-3 text-sm text-muted">This host runs step{host.steps.length === 1 ? "" : "s"} {host.steps.map((s) => s + 1).join(", ")}: only the corrections it needs.</p>
      )}
      {host.error_class && (
        <div className="mb-3">
          <Alert tone={host.state === "cancelled" || host.state === "skipped" ? "warn" : "danger"}>
            <strong>{errorExplanations[host.error_class] ?? host.error_class}</strong>
            {host.error_message && <> {host.error_message}.</>}
            {host.error_hint && <div className="mt-1">{host.error_hint}</div>}
          </Alert>
        </div>
      )}
      {steps.map((s) => <StepResult key={`r${s.index}`} step={s} />)}
      {(steps.length > 0 ? steps : [{ index: 0, truncated: false }]).map((s) => (
        <OutputPanel key={s.index} jobId={jobId} hostId={host.host_id} step={s.index} live={live}
          onOutput={onOutput} truncated={s.truncated} />
      ))}
    </Card>
  );
}

/** One step as the job page shows it: the code for commands and scripts. */
function StepText({ step }: { step: JobSpec["steps"][number] }) {
  const only = "when" in step && step.when && (step.when.os_family?.length || step.when.distro?.length)
    ? <p className="mt-1 text-xs text-muted">Only on: {[...(step.when.os_family ?? []), ...(step.when.distro ?? [])].join(", ")}</p> : null;
  const who = step.type === "command" || step.type === "script" ? (step.become ? "As root via sudo" : "As the SSH user")
    : ["wait", "verify_host", "verify_services", "verify_health"].includes(step.type) ? "Checked by DROOL (nothing runs on the host)" : "As root";
  let body;
  if (step.type === "script" && typeof step.body === "object") {
    body = (
      <div className="space-y-3">
        {Object.entries(step.body).map(([os, b]) => (
          <div key={os}>
            <div className="mb-1 text-xs font-medium uppercase text-muted">{os}</div>
            <pre className="whitespace-pre-wrap break-all font-mono text-xs">{b}</pre>
          </div>
        ))}
      </div>
    );
  } else if (step.type === "script") {
    body = <pre className="whitespace-pre-wrap break-all font-mono text-xs">{typeof step.body === "string" ? step.body : ""}</pre>;
  } else if (step.type === "command") {
    body = <pre className="whitespace-pre-wrap break-all font-mono text-xs">{step.command}</pre>;
  } else {
    body = <p className="text-sm">{describeStep(step)}</p>;
  }
  return <>{body}{only}<p className="mt-1 text-xs text-muted">{who}</p></>;
}

interface WaveRow { wave: number; label: string; names: string; status: string; done: boolean; failed: boolean }

/** Hosts grouped by rollout wave, with each wave's progress. */
function waveGroups(hosts: JobHost[], s: { canary_count?: number; canary_hosts?: string[] }): WaveRow[] {
  const by = new Map<number, JobHost[]>();
  for (const h of hosts) {
    if (h.wave < 0) continue;
    by.set(h.wave, [...(by.get(h.wave) ?? []), h]);
  }
  const ws = [...by.keys()].sort((a, b) => a - b);
  const canary = ws.length > 1 && !!(s.canary_count || s.canary_hosts?.length);
  return ws.map((w) => {
    const list = by.get(w)!;
    const ok = list.filter((h) => h.state === "succeeded").length;
    const bad = list.filter((h) => ["failed", "timed_out", "interrupted"].includes(h.state)).length;
    const waiting = list.filter((h) => h.state === "queued").length;
    const other = list.length - ok - bad - waiting;
    const done = waiting === 0 && list.every((h) => !activeStates.has(h.state));
    const parts = [ok && `${ok} succeeded`, bad && `${bad} failed`, other && !done && `${other} running`, waiting && `${waiting} not started`,
      done && other && `${other} cancelled or skipped`].filter(Boolean);
    return {
      wave: w, label: canary && w === 0 ? "Canary" : `Batch ${canary ? w : w + 1}`,
      names: list.slice(0, 6).map((h) => h.host_name).join(", ") + (list.length > 6 ? ` and ${list.length - 6} more` : ""),
      status: parts.join(", "), done, failed: bad > 0,
    };
  });
}
