import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { useMutation, useQuery } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { useAuth } from "@/auth/AuthContext";
import { useInvalidateAutomation, useOccurrences, useSchedule, useScheduleAction, minutesText, type Occurrence, type Schedule } from "@/components/automation/queries";
import { rolloutText } from "@/components/automation/Rollout";
import { OccurrenceBadge, ScheduleStateBadge } from "@/components/automation/ui";
import { fmtTime } from "@/components/monitoring/format";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { Dialog } from "@/components/ui/Dialog";
import { Field, Select } from "@/components/ui/Field";
import { PageHeader } from "@/components/ui/PageHeader";
import { plainLink } from "@/components/ui/links";
import { errorMessage } from "@/lib/format";

export function ScheduleDetailPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const { can, me } = useAuth();
  const q = useSchedule(id);
  const act = useScheduleAction(id);
  const [reassign, setReassign] = useState(false);
  if (q.error) return <Alert>{errorMessage(q.error)}</Alert>;
  const s = q.data;
  if (!s) return <p className="text-muted">Loading…</p>;
  const maint = s.kind === "maintenance";
  const mine = s.owner?.id === me?.user.id || can("schedules.admin");
  const canRun = can("schedules.manage") && (maint ? can("maintenance.manage") : can("jobs.execute"));
  const back = maint ? "/alerts?view=maintenance" : "/jobs/schedules";

  const runNow = () => {
    const what = maint ? `Start the maintenance window “${s.name}” now (for ${minutesText(s.config.duration_minutes ?? 0)})?`
      : `Run “${s.name}” now on ${s.targets_text}?\nIt runs with your permissions, and hosts under maintenance are not skipped. The next scheduled run doesn't change.`;
    if (confirm(what)) {
      act.mutate("run-now", {
        onSuccess: (r) => {
          if (r && "job_id" in r && r.job_id) navigate(`/jobs/${r.job_id}`);
        },
      });
    }
  };
  const disable = () => {
    if (!s.active_job_id || confirm("Disable this schedule? Future runs stop. The run in progress continues; cancel it on its page if you need to.")) {
      act.mutate("disable");
    }
  };
  const archive = () => {
    if (confirm(`Archive “${s.name}”? It stops running; its history and past jobs are kept.${s.active_job_id ? " The run in progress continues." : ""}`)) {
      act.mutate("archive", { onSuccess: () => navigate(back) });
    }
  };

  return (
    <>
      <PageHeader
        crumbs={maint ? [{ label: "Alerts", to: "/alerts" }, { label: "Maintenance", to: back }] : [{ label: "Schedules", to: back }]}
        title={s.name}
        description={
          <span className="inline-flex flex-wrap items-center gap-2">
            <ScheduleStateBadge s={s} />
            {maint ? "Recurring maintenance window" : s.action} · {s.when}
          </span>
        }
        actions={!s.archived && (
          <>
            {canRun && <Button onClick={runNow} disabled={act.isPending || !!s.active_job_id}>{maint ? "Start now" : "Run now"}</Button>}
            {s.enabled
              ? can("schedules.manage") && <Button onClick={disable} disabled={act.isPending}>Disable</Button>
              : mine && can("schedules.manage") && <Button variant="primary" onClick={() => act.mutate("enable")} disabled={act.isPending}>Enable</Button>}
            {mine && can("schedules.manage") && (
              <Link to={`/jobs/schedules/${s.id}/edit`} className="inline-flex h-9 items-center rounded-ui border border-line bg-surface px-3.5 text-sm font-medium hover:bg-surface-2">Edit</Link>
            )}
          </>
        )}
      />
      {act.error && <div className="mb-4"><Alert>{errorMessage(act.error)}</Alert></div>}
      {s.attention && (
        <div className="mb-4">
          <Alert>
            <strong>Needs attention:</strong> {s.attention}.{" "}
            {s.owner ? "Once that's fixed, enable the schedule again." : "An administrator can assign a new owner, then enable it again."}
          </Alert>
        </div>
      )}
      {s.workflow_update_available && (
        <div className="mb-4 flex flex-wrap items-center gap-3 rounded-ui border border-accent/40 bg-accent/10 p-3 text-sm" role="note">
          <span className="min-w-0 flex-1">
            A newer workflow revision is available: this schedule runs revision {s.config.workflow?.revision} of
            {" "}<Link className={plainLink} to={`/jobs/workflows/${s.config.workflow?.id}`}>{s.workflow_name}</Link>; the latest is {s.workflow_latest}.
            It keeps running the revision it has until you choose the new one.
          </span>
          {mine && can("schedules.manage") && <Link to={`/jobs/schedules/${s.id}/edit`} className="inline-flex h-7 items-center rounded-ui border border-line bg-surface px-2.5 text-xs font-medium hover:bg-surface-2">Review and update</Link>}
        </div>
      )}
      {s.workflow_archived && <div className="mb-4"><Alert tone="warn">Its workflow is archived. The schedule keeps running the revision it has.</Alert></div>}
      {s.active_job_id && (
        <p className="mb-4 rounded-ui border border-line bg-surface px-3 py-2 text-sm" role="status">
          A run is in progress: <Link className="text-accent underline" to={`/jobs/${s.active_job_id}`}>view it</Link>.
          Disabling the schedule doesn't stop it; cancelling it doesn't disable the schedule.
        </p>
      )}
      {s.active_maintenance_id && (
        <p className="mb-4 rounded-ui border border-line bg-surface px-3 py-2 text-sm" role="status">
          This window is active now. <Link className="text-accent underline" to="/alerts?view=maintenance">See maintenance</Link>.
        </p>
      )}

      <div className="grid gap-4 xl:grid-cols-[22rem_1fr]">
        <Card title="Settings">
          <Summary s={s} />
          {can("schedules.admin") && !s.archived && (
            <div className="mt-4 flex flex-wrap gap-2 border-t border-line pt-3">
              <Button size="sm" onClick={() => setReassign(true)}>Change owner</Button>
              {mine && <Button size="sm" variant="ghost" onClick={archive}>Archive</Button>}
            </div>
          )}
          {!can("schedules.admin") && mine && !s.archived && (
            <div className="mt-4 border-t border-line pt-3"><Button size="sm" variant="ghost" onClick={archive}>Archive</Button></div>
          )}
        </Card>
        <History id={s.id} maint={maint} />
      </div>
      {reassign && <ReassignDialog s={s} onClose={() => setReassign(false)} />}
    </>
  );
}

function Summary({ s }: { s: Schedule }) {
  const c = s.config;
  const maint = s.kind === "maintenance";
  const rows: [string, React.ReactNode][] = [
    ["When", <>{s.when}</>],
    ["Next run", s.enabled && s.next_run_local ? <>{s.next_run_local}{s.next_run_note && <span className="block text-xs text-warn">{s.next_run_note}</span>}</> : "—"],
    ["Targets", <>{s.targets_text}<span className="block text-xs text-muted">Resolved again at every run.</span></>],
  ];
  if (maint) {
    rows.push(["Each window", minutesText(c.duration_minutes ?? 0)], ["Reason", c.reason || "—"]);
  } else {
    rows.push(
      ["Runs", c.workflow
        ? <><Link className="text-accent" to={`/jobs/workflows/${c.workflow.id}`}>{s.workflow_name || "workflow"}</Link>, revision {c.workflow.revision}</>
        : s.action],
      ["Rollout", rolloutText(c.strategy ?? { concurrency: 10 })],
      ["If DROOL was down", c.recurrence.kind === "once"
        ? (c.late_limit_minutes ? `Starts when DROOL returns, unless more than ${c.late_limit_minutes} min late` : "Starts when DROOL returns")
        : c.missed_policy === "run_once" ? "Runs the most recent missed run once" : "Skips missed runs"],
      ["If still running", c.overlap_policy === "queue_one" ? "Queues one run" : "Skips the new run"],
      ["Maintenance window", s.window_name ? <Link className="text-accent" to={`/jobs/schedules/${c.window_id}`}>{s.window_name}</Link> : "None (hosts under maintenance are skipped)"],
    );
  }
  rows.push(
    ["Owner", s.owner ? <>{s.owner.name}<span className="block text-xs text-muted">Scheduled runs use this person's permissions, checked before every run.</span></> : <span className="text-danger">None (the account was deleted)</span>],
    ["Revision", <>{s.revision}{s.updated_by && <span className="text-muted"> · changed by {s.updated_by}</span>}<span className="block text-xs text-muted">{fmtTime(s.updated_at)}</span></>],
  );
  return (
    <dl className="grid grid-cols-[7rem_1fr] sm:grid-cols-[8rem_1fr] gap-x-3 gap-y-2 text-sm">
      {rows.map(([k, v]) => (
        <div key={k} className="contents">
          <dt className="text-muted">{k}</dt>
          <dd className="min-w-0">{v}</dd>
        </div>
      ))}
    </dl>
  );
}

function History({ id, maint }: { id: string; maint: boolean }) {
  const q = useOccurrences(id);
  const rows = q.data?.pages.flatMap((p) => p.items) ?? [];
  return (
    <Card title="History">
      <p className="mb-3 text-xs text-muted">Every time this schedule was due: when, what happened and why. {maint ? "" : "Each run is also an ordinary job under Runs."}</p>
      {q.error && <Alert>{errorMessage(q.error)}</Alert>}
      <div className="overflow-x-auto">
        <table className="w-full text-left text-sm">
          <caption className="sr-only">Schedule history</caption>
          <thead>
            <tr className="border-b border-line text-xs uppercase tracking-wide text-muted">
              <th className="px-3 py-2 font-medium">Scheduled for</th>
              <th className="px-3 py-2 font-medium">Started</th>
              <th className="px-3 py-2 font-medium">Outcome</th>
              <th className="px-3 py-2 font-medium">{maint ? "" : "Run"}</th>
            </tr>
          </thead>
          <tbody>
            {rows.length === 0 ? (
              <tr><td colSpan={4} className="px-3 py-6 text-center text-muted">{q.isPending ? "Loading…" : "Nothing yet. The first run appears here when it's due."}</td></tr>
            ) : rows.map((o) => <OccurrenceRow key={o.id} o={o} />)}
          </tbody>
        </table>
      </div>
      {q.hasNextPage && (
        <div className="mt-3 flex justify-center">
          <Button onClick={() => void q.fetchNextPage()} disabled={q.isFetchingNextPage}>{q.isFetchingNextPage ? "Loading…" : "Load more"}</Button>
        </div>
      )}
    </Card>
  );
}

function OccurrenceRow({ o }: { o: Occurrence }) {
  return (
    <tr className="border-b border-line align-top last:border-0">
      <td className="whitespace-nowrap px-3 py-2">
        {o.manual ? <span className="text-muted">—</span> : fmtTime(o.scheduled_for)}
        {o.manual && <span className="block text-xs text-muted">Run now{o.triggered_by ? ` by ${o.triggered_by}` : ""}</span>}
      </td>
      <td className="whitespace-nowrap px-3 py-2 text-muted">{o.dispatched_at ? fmtTime(o.dispatched_at) : "—"}</td>
      <td className="px-3 py-2">
        <div className="flex flex-col items-start gap-1">
          <OccurrenceBadge o={o} />
          <span>{o.outcome}</span>
          {o.status === "dispatched" && o.reason && <span className="text-xs text-muted">{o.reason}</span>}
          {o.skipped_hosts > 0 && <span className="text-xs text-muted">{o.skipped_hosts} host{o.skipped_hosts === 1 ? "" : "s"} skipped: maintenance active</span>}
        </div>
      </td>
      <td className="px-3 py-2">{o.job_id && <Link className="text-accent" to={`/jobs/${o.job_id}`}>View run</Link>}</td>
    </tr>
  );
}

function ReassignDialog({ s, onClose }: { s: Schedule; onClose: () => void }) {
  const inv = useInvalidateAutomation();
  const users = useQuery({ queryKey: ["users"], queryFn: () => unwrap(api.GET("/users")) });
  const [userId, setUserId] = useState(s.owner?.id ?? "");
  const save = useMutation({
    mutationFn: () => unwrap(api.POST("/schedules/{id}/owner", { params: { path: { id: s.id } }, body: { user_id: userId } })),
    onSuccess: () => { inv(); onClose(); },
  });
  return (
    <Dialog title="Change owner" onClose={onClose}>
      <form className="space-y-4" onSubmit={(e) => { e.preventDefault(); save.mutate(); }}>
        <p className="text-sm text-muted">Scheduled runs use the owner's permissions. The new owner must be allowed to run what this schedule runs.</p>
        <Field label="Owner">
          <Select value={userId} onChange={(e) => setUserId(e.target.value)} data-autofocus>
            <option value="">Choose…</option>
            {(users.data?.items ?? []).filter((u) => !u.disabled).map((u) => <option key={u.id} value={u.id}>{u.username} ({u.role})</option>)}
          </Select>
        </Field>
        {save.error && <Alert>{errorMessage(save.error)}</Alert>}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={!userId || save.isPending}>Change owner</Button>
        </div>
      </form>
    </Dialog>
  );
}
