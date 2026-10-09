import { useState } from "react";
import { Link } from "react-router";
import { useAuth } from "@/auth/AuthContext";
import { useSchedules, type Schedule } from "@/components/automation/queries";
import { OccurrenceBadge, ScheduleStateBadge } from "@/components/automation/ui";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { EmptyState } from "@/components/ui/EmptyState";
import { PageHeader } from "@/components/ui/PageHeader";
import { Table } from "@/components/ui/Table";
import { errorMessage } from "@/lib/format";
import { JobsTabs } from "@/pages/jobs/JobsTabs";

/** Jobs → Schedules: what runs when, and whether it worked last time. */
export function SchedulesPage() {
  const { can } = useAuth();
  const [archived, setArchived] = useState(false);
  const list = useSchedules("job", archived);
  const rows = list.data?.items ?? [];
  const newButton = can("schedules.manage") && can("jobs.execute") && (
    <Link to="/jobs/schedules/new" className="inline-flex h-9 items-center rounded-ui bg-accent px-3.5 text-sm font-medium text-accent-fg hover:opacity-90">
      New schedule
    </Link>
  );
  const attention = rows.filter((s) => s.attention).length;
  return (
    <>
      <PageHeader title="Jobs" actions={newButton} />
      <JobsTabs />
      {list.error && <Alert>{errorMessage(list.error)}</Alert>}
      {!archived && list.data && rows.length === 0 ? (
        <EmptyState title="No schedules yet"
          body="A schedule runs a job or a workflow at the times you choose, for example every Sunday at 03:00. Every run is an ordinary job you'll find under Runs."
          action={newButton} />
      ) : (
        <Card title={archived ? "Archived schedules" : "Schedules"}
          actions={<Button size="sm" variant="ghost" onClick={() => setArchived(!archived)}>{archived ? "Show current" : "Show archived"}</Button>}>
          {attention > 0 && (
            <div className="mb-3"><Alert tone="warn">{attention} schedule{attention === 1 ? " needs" : "s need"} attention: {attention === 1 ? "it isn't" : "they aren't"} running. Open {attention === 1 ? "it" : "them"} to see why.</Alert></div>
          )}
          <Table<Schedule>
            rows={rows}
            rowKey={(s) => s.id}
            empty={list.isPending ? "Loading…" : "No schedules."}
            columns={[
              {
                header: "Schedule",
                cell: (s) => (
                  <div className="min-w-[10rem]">
                    <Link className="font-medium text-accent" to={`/jobs/schedules/${s.id}`}>{s.name}</Link>
                    {s.attention && <div className="text-xs text-danger">{s.attention}</div>}
                  </div>
                ),
              },
              { header: "State", cell: (s) => <ScheduleStateBadge s={s} /> },
              {
                header: "Next run",
                cell: (s) => s.enabled && s.next_run_local
                  ? <span className="whitespace-nowrap">{s.next_run_local}{s.next_run_note && <span className="block text-xs text-warn">{s.next_run_note}</span>}</span>
                  : <span className="text-muted">—</span>,
              },
              { header: "When", cell: (s) => <span className="text-muted">{s.when}</span> },
              { header: "Targets", cell: (s) => <span className="text-muted">{s.targets_text}</span> },
              {
                header: "Runs",
                cell: (s) => (
                  <span>
                    {s.action}
                    {s.workflow_update_available && <span className="ml-1"><Badge tone="accent">newer revision</Badge></span>}
                  </span>
                ),
              },
              {
                header: "Last result",
                cell: (s) => s.last_occurrence ? (
                  <span className="flex flex-col items-start gap-0.5">
                    <OccurrenceBadge o={s.last_occurrence} />
                    {s.last_occurrence.job_id && <Link className="text-xs text-accent" to={`/jobs/${s.last_occurrence.job_id}`}>view run</Link>}
                  </span>
                ) : <span className="text-muted">never ran</span>,
              },
              { header: "Owner", cell: (s) => s.owner?.name ?? <span className="text-danger">none</span> },
            ]}
          />
        </Card>
      )}
    </>
  );
}
