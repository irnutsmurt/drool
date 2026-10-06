import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { jk, useJob, useJobHost, useJobHosts, type JobHost } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { useJobEvents } from "@/hooks/useJobEvents";
import { HostStateBadge, JobStatusBadge, errorExplanations } from "@/components/jobs/badges";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { PageHeader } from "@/components/ui/PageHeader";
import { Table } from "@/components/ui/Table";
import { cx } from "@/components/ui/cx";
import { errorMessage, formatDateTime } from "@/lib/format";
import { OutputPanel } from "./OutputPanel";
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

  if (job.error) return <Alert>{errorMessage(job.error)}</Alert>;
  const j = job.data;
  if (!j) return <p className="text-muted">Loading…</p>;
  const items = hosts.data?.items ?? [];
  const current = items.find((h) => h.host_id === selected) ?? (items.length === 1 ? items[0] : undefined);
  const failedCount = (j.counts.failed ?? 0) + (j.counts.timed_out ?? 0) + (j.counts.cancelled ?? 0) +
    (j.counts.skipped ?? 0) + (j.counts.interrupted ?? 0);
  const step = j.spec.steps[0];

  return (
    <>
      <PageHeader
        title={j.name}
        description={
          <span className="inline-flex flex-wrap items-center gap-2">
            <JobStatusBadge job={j} /> {summarizeCounts(j)} · started {formatDateTime(j.created_at)} by {j.created_by}
            {j.rerun_of && <>· <Link className="text-accent" to={`/jobs/${j.rerun_of}`}>re-run of earlier job</Link></>}
          </span>
        }
        actions={can("jobs.execute") && (
          <>
            {j.status === "paused" && <Button variant="primary" onClick={() => action.mutate("resume")}>Resume</Button>}
            {active && (
              <Button variant="danger" disabled={action.isPending}
                onClick={() => { if (confirm("Cancel this job? Running commands will be terminated.")) action.mutate("cancel"); }}>
                Cancel
              </Button>
            )}
            {!active && failedCount > 0 && (
              <Button onClick={() => action.mutate("rerun")} disabled={action.isPending}>Re-run unsuccessful hosts</Button>
            )}
          </>
        )}
      />
      {j.status === "paused" && <div className="mb-4"><Alert tone="warn">Paused: {j.pause_reason}. Fix the cause, then resume or cancel.</Alert></div>}
      {j.status === "interrupted" && (
        <div className="mb-4"><Alert tone="warn">The controller restarted while this job was running. Hosts that were in progress have an unknown outcome.</Alert></div>
      )}
      {action.error && <div className="mb-4"><Alert>{errorMessage(action.error)}</Alert></div>}

      <div className="grid gap-4">
        <Card title="What ran">
          <pre className="whitespace-pre-wrap break-all font-mono text-xs">
            {step?.type === "command" ? step.command : step?.type === "script" ? step.body : ""}
          </pre>
          <p className="mt-2 text-xs text-muted">
            {step && "become" in step && step.become ? "As root via sudo" : "As the SSH user"} · {j.spec.strategy?.concurrency ?? 10} hosts at a time
          </p>
        </Card>

        <Card title="Hosts">
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
              { header: "Exit", cell: (h) => (h.exit_code ?? "—") },
              { header: "Time", cell: (h) => <span className="text-muted">{duration(h)}</span> },
              { header: "Problem", cell: (h) => <span className="text-muted">{h.error_message}</span> },
            ]}
          />
        </Card>

        {current ? (
          <HostOutput jobId={id} host={current} live={active && activeStates.has(current.state)} onOutput={events.onOutput} />
        ) : (
          items.length > 1 && <p className="text-sm text-muted">Select a host to see its output.</p>
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
      {host.error_class && (
        <div className="mb-3">
          <Alert tone={host.state === "cancelled" || host.state === "skipped" ? "warn" : "danger"}>
            <strong>{errorExplanations[host.error_class] ?? host.error_class}</strong>
            {host.error_message && <> {host.error_message}.</>}
            {host.error_hint && <div className="mt-1">{host.error_hint}</div>}
          </Alert>
        </div>
      )}
      {(steps.length > 0 ? steps : [{ index: 0, truncated: false }]).map((s) => (
        <OutputPanel key={s.index} jobId={jobId} hostId={host.host_id} step={s.index} live={live}
          onOutput={onOutput} truncated={s.truncated} />
      ))}
    </Card>
  );
}
